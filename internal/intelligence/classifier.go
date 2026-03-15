// @nexus-project: nexus
// @nexus-path: internal/intelligence/classifier.go
// Package intelligence — Naive Bayes classifier for Drop Intelligence layer 5.
//
// ALGORITHM — Multinomial Naive Bayes (text classification):
//   Each project gets a probability model built from the token frequencies of
//   filenames that were successfully routed to it (download_log WHERE action='moved').
//   At classification time, a new filename is tokenised and scored against
//   every project model. The project with the highest log-probability wins.
//
// WHY NAIVE BAYES, NOT LOGISTIC REGRESSION:
//   Naive Bayes needs very few training examples (10–20 per project is enough),
//   trains in microseconds, and requires zero matrix operations or gradient
//   descent. For a filename classifier with sparse features this is optimal.
//   Logistic regression would require significantly more data to converge.
//
// TRAINING DATA SOURCE:
//   download_log table, rows where action = 'moved' or 'approved'.
//   These are confirmed correct routes — the ground truth.
//   Rejected files (action = 'rejected') are ignored — we don't want to
//   learn from routing decisions that the user overrode.
//
// MODEL PERSISTENCE:
//   Saved to ~/.nexus/classifier.json after each training run.
//   Loaded at daemon startup. If the file does not exist, the classifier
//   is inactive and layer 5 contributes zero confidence.
//   Model is small — typically a few KB even with hundreds of training files.
//
// TOKENISATION:
//   Filename (without extension) is split on [_\-. ] into lowercase tokens.
//   Tokens shorter than 2 characters are discarded (noise).
//   Numbers-only tokens are discarded.
//   Example: "nexus__drop-router__20260314_1400.go"
//   → ["nexus", "drop", "router", "20260314", "1400"] → after filter → ["nexus", "drop", "router"]
package intelligence

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	modelFileName   = ".nexus/classifier.json"
	minTokenLength  = 2
	laplaceSmoother = 1.0 // Laplace (add-1) smoothing — prevents zero probabilities
)

// tokenSplitter splits on underscore, hyphen, dot, or whitespace.
var tokenSplitter = regexp.MustCompile(`[_\-.\s]+`)

// digitsOnly matches tokens that are entirely numeric.
var digitsOnly = regexp.MustCompile(`^\d+$`)

// ── MODEL ─────────────────────────────────────────────────────────────────────

// ClassifierModel is the serialised form saved to disk.
// All fields are exported for JSON marshalling.
type ClassifierModel struct {
	// TokenCounts[projectID][token] = count of that token in training docs for this project.
	TokenCounts map[string]map[string]int `json:"token_counts"`

	// DocCounts[projectID] = number of training documents for this project.
	DocCounts map[string]int `json:"doc_counts"`

	// Vocab is the set of all unique tokens across all projects.
	// Needed for Laplace smoothing denominator.
	Vocab map[string]struct{} `json:"vocab"`

	// TotalDocs is the total number of training documents across all projects.
	TotalDocs int `json:"total_docs"`

	// TrainedAt is an RFC3339 timestamp for the UI.
	TrainedAt string `json:"trained_at"`
}

// ── CLASSIFIER ────────────────────────────────────────────────────────────────

// Classifier performs Naive Bayes classification on filenames.
// A nil or untrained Classifier always returns Confidence=0 — safe to use
// without checking. Wrap in Detector as optional layer 5.
type Classifier struct {
	model    *ClassifierModel
	modelDir string
}

// NewClassifier creates a Classifier and attempts to load an existing model
// from disk. If no model exists, the Classifier is valid but inactive.
func NewClassifier() *Classifier {
	home, err := os.UserHomeDir()
	if err != nil {
		return &Classifier{}
	}
	c := &Classifier{modelDir: filepath.Join(home, filepath.Dir(modelFileName))}

	// Best-effort load — failure leaves classifier inactive, not broken.
	modelPath := filepath.Join(home, modelFileName)
	data, err := os.ReadFile(modelPath)
	if err != nil {
		return c // no model yet — Train() creates it
	}

	var m ClassifierModel
	if err := json.Unmarshal(data, &m); err != nil {
		return c // corrupt model — will be overwritten on next Train()
	}
	// Re-hydrate Vocab (JSON marshals map[string]struct{} oddly — ensure non-nil).
	if m.Vocab == nil {
		m.Vocab = map[string]struct{}{}
	}
	c.model = &m
	return c
}

// ── CLASSIFY ─────────────────────────────────────────────────────────────────

// ClassifyResult is the output of classifying one filename.
type ClassifyResult struct {
	ProjectID  string
	Confidence float64 // 0.0–1.0
}

// Classify scores a filename against all trained project models.
// Returns zero confidence if the model is untrained or no projects match.
// Never returns an error — classification failure degrades to zero confidence.
func (c *Classifier) Classify(fileName string) ClassifyResult {
	if c.model == nil || len(c.model.DocCounts) == 0 {
		return ClassifyResult{}
	}

	tokens := tokenise(strings.TrimSuffix(fileName, filepath.Ext(fileName)))
	if len(tokens) == 0 {
		return ClassifyResult{}
	}

	vocabSize := float64(len(c.model.Vocab))
	bestProject := ""
	bestScore := math.Inf(-1)

	for projectID, tokenCounts := range c.model.TokenCounts {
		docCount := c.model.DocCounts[projectID]
		if docCount == 0 {
			continue
		}

		// Log prior: log P(project) = log(docCount / totalDocs)
		logProb := math.Log(float64(docCount) / float64(c.model.TotalDocs))

		// Total tokens in this project's training corpus.
		totalTokens := 0
		for _, cnt := range tokenCounts {
			totalTokens += cnt
		}

		// Log likelihood: Σ log P(token | project) with Laplace smoothing.
		for _, token := range tokens {
			tokenCount := float64(tokenCounts[token])
			// P(token|project) = (count + α) / (totalTokens + α * vocabSize)
			logProb += math.Log((tokenCount + laplaceSmoother) /
				(float64(totalTokens) + laplaceSmoother*vocabSize))
		}

		if logProb > bestScore {
			bestScore = logProb
			bestProject = projectID
		}
	}

	if bestProject == "" {
		return ClassifyResult{}
	}

	// Convert log-probability to a [0,1] confidence using sigmoid.
	// Raw log-probs are negative; sigmoid maps them to a usable range.
	confidence := sigmoid(bestScore)
	return ClassifyResult{ProjectID: bestProject, Confidence: confidence}
}

// ── TRAIN ─────────────────────────────────────────────────────────────────────

// TrainingExample is one record from download_log.
type TrainingExample struct {
	FileName  string
	ProjectID string
}

// Train builds a new model from the provided examples and saves it to disk.
// Replaces any previously saved model.
// Returns the number of examples used and any save error.
func (c *Classifier) Train(examples []TrainingExample, trainedAt string) (int, error) {
	model := &ClassifierModel{
		TokenCounts: make(map[string]map[string]int),
		DocCounts:   make(map[string]int),
		Vocab:       make(map[string]struct{}),
		TrainedAt:   trainedAt,
	}

	usable := 0
	for _, ex := range examples {
		if ex.ProjectID == "" || ex.FileName == "" {
			continue
		}

		tokens := tokenise(strings.TrimSuffix(ex.FileName, filepath.Ext(ex.FileName)))
		if len(tokens) == 0 {
			continue
		}

		if model.TokenCounts[ex.ProjectID] == nil {
			model.TokenCounts[ex.ProjectID] = make(map[string]int)
		}

		for _, token := range tokens {
			model.TokenCounts[ex.ProjectID][token]++
			model.Vocab[token] = struct{}{}
		}

		model.DocCounts[ex.ProjectID]++
		model.TotalDocs++
		usable++
	}

	if usable == 0 {
		return 0, nil // nothing to train on — leave model unchanged
	}

	c.model = model
	return usable, c.saveModel()
}

// ModelInfo returns a summary of the current model for display.
// Returns nil if untrained.
func (c *Classifier) ModelInfo() map[string]any {
	if c.model == nil {
		return nil
	}
	projectCounts := make(map[string]int, len(c.model.DocCounts))
	for k, v := range c.model.DocCounts {
		projectCounts[k] = v
	}
	return map[string]any{
		"trained_at":    c.model.TrainedAt,
		"total_docs":    c.model.TotalDocs,
		"vocab_size":    len(c.model.Vocab),
		"project_docs":  projectCounts,
	}
}

// ── PERSISTENCE ───────────────────────────────────────────────────────────────

func (c *Classifier) saveModel() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.modelDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c.model, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, modelFileName), data, 0644)
}

// ── TOKENISATION ──────────────────────────────────────────────────────────────

// tokenise splits a filename stem into lowercase tokens, discarding noise.
func tokenise(stem string) []string {
	parts := tokenSplitter.Split(strings.ToLower(stem), -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) < minTokenLength {
			continue
		}
		if digitsOnly.MatchString(p) {
			continue // timestamps, version numbers — not useful features
		}
		out = append(out, p)
	}
	return out
}

// ── MATH ──────────────────────────────────────────────────────────────────────

// sigmoid maps a value to [0,1]. Used to convert log-probabilities to confidence.
// Tuned so that typical log-prob ranges map to the 0.30–0.70 confidence band.
func sigmoid(x float64) float64 {
	// Scale: log-probs from Naive Bayes on short filenames are typically -5 to -30.
	// Shift by +15 and scale by 0.3 so the midpoint lands around 0.5.
	scaled := (x + 15.0) * 0.3
	return 1.0 / (1.0 + math.Exp(-scaled))
}
