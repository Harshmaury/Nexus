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
//
// TRAINING DATA SOURCE:
//   download_log table, rows where action = 'moved' or 'approved'.
//   Rejected files (action = 'rejected') are ignored — we don't learn from
//   routing decisions the user overrode.
//
// MODEL PERSISTENCE:
//   Saved to ~/.nexus/classifier.json after each training run.
//   Loaded at daemon startup. If the file does not exist, the classifier
//   is inactive and layer 5 contributes zero confidence.
//
// TOKENISATION:
//   Filename (without extension) is split on [_\-. ] into lowercase tokens.
//   Tokens shorter than 2 characters are discarded (noise).
//   Numbers-only tokens are discarded.
//   Example: "nexus__drop-router__20260314_1400.go"
//   → ["nexus", "drop", "router", "20260314", "1400"] → after filter → ["nexus", "drop", "router"]
//
// NX-Fix-03: Classifier is now goroutine-safe.
//   Previously Train() replaced c.model with a bare pointer assignment while
//   Classify() and ModelInfo() read c.model concurrently from the intelligence
//   pipeline goroutine — a data race under -race.
//
//   Fix: a sync.RWMutex guards all access to c.model.
//     Train()     acquires a write lock for the pointer swap only.
//                 Model construction runs before the lock — training time is
//                 unchanged; only the final atomic swap is serialised.
//     Classify()  acquires a read lock, snapshots the pointer, releases the
//                 lock, then reads the immutable model without holding the lock.
//                 Concurrent classifications never block each other.
//     ModelInfo() same read-lock + snapshot pattern as Classify().
//     saveModel() called outside the lock — disk I/O never holds the mutex.
package intelligence

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
// Once stored in Classifier.model a ClassifierModel is never mutated —
// Train() always builds a fresh model and swaps the pointer atomically.
type ClassifierModel struct {
	// TokenCounts[projectID][token] = count of that token in training docs.
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
// Safe for concurrent use — multiple goroutines may call Classify and
// ModelInfo simultaneously, and Train may be called while classification
// is in progress.
//
// A nil or untrained Classifier always returns Confidence=0.
type Classifier struct {
	mu       sync.RWMutex
	model    *ClassifierModel // guarded by mu; replaced atomically on Train
	modelDir string
}

// NewClassifier creates a Classifier and attempts to load an existing model
// from disk. If no model exists the Classifier is valid but inactive.
func NewClassifier() *Classifier {
	home, err := os.UserHomeDir()
	if err != nil {
		return &Classifier{}
	}
	c := &Classifier{modelDir: filepath.Join(home, filepath.Dir(modelFileName))}

	// Best-effort load — failure leaves classifier inactive, not broken.
	data, err := os.ReadFile(filepath.Join(home, modelFileName))
	if err != nil {
		return c
	}

	var m ClassifierModel
	if err := json.Unmarshal(data, &m); err != nil {
		return c // corrupt model — overwritten on next Train()
	}
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
// Safe for concurrent use.
func (c *Classifier) Classify(fileName string) ClassifyResult {
	c.mu.RLock()
	m := c.model // snapshot the pointer
	c.mu.RUnlock()

	if m == nil || len(m.DocCounts) == 0 {
		return ClassifyResult{}
	}

	tokens := tokenise(strings.TrimSuffix(fileName, filepath.Ext(fileName)))
	if len(tokens) == 0 {
		return ClassifyResult{}
	}

	vocabSize    := float64(len(m.Vocab))
	bestProject  := ""
	bestScore    := math.Inf(-1)

	for projectID, tokenCounts := range m.TokenCounts {
		docCount := m.DocCounts[projectID]
		if docCount == 0 {
			continue
		}

		// Log prior: log P(project) = log(docCount / totalDocs)
		logProb := math.Log(float64(docCount) / float64(m.TotalDocs))

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
			bestScore    = logProb
			bestProject  = projectID
		}
	}

	if bestProject == "" {
		return ClassifyResult{}
	}

	return ClassifyResult{ProjectID: bestProject, Confidence: sigmoid(bestScore)}
}

// ── TRAIN ─────────────────────────────────────────────────────────────────────

// TrainingExample is one record from download_log.
type TrainingExample struct {
	FileName  string
	ProjectID string
}

// Train builds a new model from the provided examples and saves it to disk.
// The new model replaces the previous one atomically — callers blocked on
// Classify() finish against the old model; subsequent calls use the new one.
// Returns the number of examples used and any save error.
// Safe for concurrent use.
func (c *Classifier) Train(examples []TrainingExample, trainedAt string) (int, error) {
	// Build the model without holding the lock — this is the slow part.
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
		return 0, nil // nothing to train on — leave existing model unchanged
	}

	// Atomic pointer swap — only this assignment is serialised.
	c.mu.Lock()
	c.model = model
	c.mu.Unlock()

	// Save to disk outside the lock — disk I/O must not block Classify().
	return usable, c.saveModel(model)
}

// ModelInfo returns a summary of the current model for display.
// Returns nil if untrained. Safe for concurrent use.
func (c *Classifier) ModelInfo() map[string]any {
	c.mu.RLock()
	m := c.model // snapshot
	c.mu.RUnlock()

	if m == nil {
		return nil
	}

	projectCounts := make(map[string]int, len(m.DocCounts))
	for k, v := range m.DocCounts {
		projectCounts[k] = v
	}
	return map[string]any{
		"trained_at":   m.TrainedAt,
		"total_docs":   m.TotalDocs,
		"vocab_size":   len(m.Vocab),
		"project_docs": projectCounts,
	}
}

// ── PERSISTENCE ───────────────────────────────────────────────────────────────

// saveModel writes m to disk. Called by Train() after the pointer swap,
// outside the lock so disk I/O never blocks Classify().
func (c *Classifier) saveModel(m *ClassifierModel) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.modelDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
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
			continue
		}
		out = append(out, p)
	}
	return out
}

// ── MATH ──────────────────────────────────────────────────────────────────────

// sigmoid maps a value to [0,1]. Used to convert log-probabilities to confidence.
// Tuned so that typical log-prob ranges map to the 0.30–0.70 confidence band.
func sigmoid(x float64) float64 {
	scaled := (x + 15.0) * 0.3
	return 1.0 / (1.0 + math.Exp(-scaled))
}
