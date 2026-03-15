// @nexus-project: nexus
// @nexus-path: internal/intelligence/detector.go
// Package intelligence — Detector runs scoring layers against a file.
//
// Phase 13 addition — Layer 5 (ML classifier):
//   Detector now accepts an optional *Classifier. If provided and trained,
//   layer 5 runs after layers 1–4 and contributes up to weightML = 0.30.
//   Total weight budget is still capped at 1.0 at output.
//
//   Layer 5 only activates if:
//     a) A Classifier was provided at construction time
//     b) The model is trained (TotalDocs > 0)
//     c) Confidence has not already hit the certainty threshold
//
//   If no project has been identified by layers 1–4 and the classifier
//   has a confident match, layer 5 can independently identify the project.
package intelligence

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	weightFilenamePrefix = 0.50
	weightHeaderComment  = 0.40
	weightContentScan    = 0.20
	weightExtensionHints = 0.10
	weightML             = 0.30 // layer 5 — Naive Bayes classifier

	certaintyThreshold = 1.0
	maxHeaderLines     = 5
	maxContentLines    = 100
)

// ── DETECTION RESULT ─────────────────────────────────────────────────────────

type DetectionResult struct {
	FilePath   string
	FileName   string
	ProjectID  string
	TargetPath string
	Confidence float64
	Method     string
	Signals    []string
}

// ── DETECTOR ─────────────────────────────────────────────────────────────────

// Detector scores files using a weighted multi-layer model.
// Each layer is independent — no layer depends on another's output.
// Pass a trained *Classifier to enable layer 5; pass nil to skip it.
type Detector struct {
	registeredProjects map[string][]string
	classifier         *Classifier // optional — nil disables layer 5
}

// NewDetector creates a Detector with a known project registry.
// projectKeywords: map[projectID][]keywords
// classifier: pass NewClassifier() for layer 5, or nil to disable it.
func NewDetector(projectKeywords map[string][]string, classifier *Classifier) *Detector {
	return &Detector{
		registeredProjects: projectKeywords,
		classifier:         classifier,
	}
}

// ── DETECT ───────────────────────────────────────────────────────────────────

// Detect runs all active scoring layers and returns the result.
func (d *Detector) Detect(filePath string) DetectionResult {
	result := DetectionResult{
		FilePath: filePath,
		FileName: filepath.Base(filePath),
	}

	// Layer 1 — Filename prefix (weight: 0.50)
	if projectID, targetPath, ok := d.scoreFilenamePrefix(result.FileName); ok {
		result.Confidence += weightFilenamePrefix
		result.ProjectID = projectID
		result.TargetPath = targetPath
		result.Method = "filename-prefix"
		result.Signals = append(result.Signals, "filename follows [project]__[feature]__ convention")
		if result.Confidence >= certaintyThreshold {
			result.Confidence = minF(result.Confidence, 1.0)
			return result
		}
	}

	// Layer 2 — Header comment (weight: 0.40)
	if projectID, targetPath, ok := d.scoreHeaderComment(filePath); ok {
		result.Confidence += weightHeaderComment
		if result.ProjectID == "" {
			result.ProjectID = projectID
		}
		if result.TargetPath == "" {
			result.TargetPath = targetPath
		}
		result.Method = appendMethod(result.Method, "header-comment")
		result.Signals = append(result.Signals, "found @nexus-project and @nexus-path in header")
		if result.Confidence >= certaintyThreshold {
			result.Confidence = minF(result.Confidence, 1.0)
			return result
		}
	}

	// Layer 3 — Content scan (weight: 0.20)
	if projectID, ok := d.scoreContentScan(filePath); ok {
		result.Confidence += weightContentScan
		if result.ProjectID == "" {
			result.ProjectID = projectID
		}
		result.Method = appendMethod(result.Method, "content-scan")
		result.Signals = append(result.Signals, "matched project keywords in file content")
	}

	// Layer 4 — Extension hints (weight: 0.10)
	if projectID, ok := d.scoreExtensionHints(result.FileName); ok {
		result.Confidence += weightExtensionHints
		if result.ProjectID == "" {
			result.ProjectID = projectID
		}
		result.Method = appendMethod(result.Method, "extension-hint")
		result.Signals = append(result.Signals, "file extension matches known project language")
	}

	// Layer 5 — ML classifier (weight: up to 0.30)
	if d.classifier != nil && result.Confidence < certaintyThreshold {
		classified := d.classifier.Classify(result.FileName)
		if classified.Confidence > 0 {
			contribution := weightML * classified.Confidence
			result.Confidence += contribution
			if result.ProjectID == "" {
				result.ProjectID = classified.ProjectID
			}
			result.Method = appendMethod(result.Method, "ml-classifier")
			result.Signals = append(result.Signals,
				"ml classifier matched "+classified.ProjectID+
					" with confidence "+formatPct(classified.Confidence))
		}
	}

	result.Confidence = minF(result.Confidence, 1.0)
	return result
}

// ── LAYER 1: FILENAME PREFIX ─────────────────────────────────────────────────

func (d *Detector) scoreFilenamePrefix(fileName string) (string, string, bool) {
	name := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	parts := strings.SplitN(name, "__", 3)
	if len(parts) < 2 {
		return "", "", false
	}
	projectID := strings.ToLower(strings.TrimSpace(parts[0]))
	if projectID == "" {
		return "", "", false
	}
	if _, known := d.registeredProjects[projectID]; !known {
		return "", "", false
	}
	return projectID, "", true
}

// ── LAYER 2: HEADER COMMENT ──────────────────────────────────────────────────

func (d *Detector) scoreHeaderComment(filePath string) (string, string, bool) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", "", false
	}
	defer file.Close()

	var projectID, targetPath string
	scanner := bufio.NewScanner(file)
	lineCount := 0

	for scanner.Scan() && lineCount < maxHeaderLines {
		line := strings.TrimSpace(scanner.Text())
		lineCount++
		if projectID == "" {
			if val, found := extractAnnotation(line, "@nexus-project:"); found {
				projectID = val
			}
		}
		if targetPath == "" {
			if val, found := extractAnnotation(line, "@nexus-path:"); found {
				targetPath = val
			}
		}
		if projectID != "" && targetPath != "" {
			break
		}
	}

	if projectID == "" {
		return "", "", false
	}
	if _, known := d.registeredProjects[projectID]; !known {
		return "", "", false
	}
	return projectID, targetPath, true
}

// ── LAYER 3: CONTENT SCAN ────────────────────────────────────────────────────

func (d *Detector) scoreContentScan(filePath string) (string, bool) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", false
	}
	defer file.Close()

	var sb strings.Builder
	scanner := bufio.NewScanner(file)
	lineCount := 0
	for scanner.Scan() && lineCount < maxContentLines {
		sb.WriteString(strings.ToLower(scanner.Text()))
		sb.WriteString("\n")
		lineCount++
	}
	content := sb.String()

	for projectID, keywords := range d.registeredProjects {
		matchCount := 0
		for _, keyword := range keywords {
			if strings.Contains(content, strings.ToLower(keyword)) {
				matchCount++
			}
		}
		if matchCount >= 2 {
			return projectID, true
		}
	}
	return "", false
}

// ── LAYER 4: EXTENSION HINTS ─────────────────────────────────────────────────

var extensionToLanguage = map[string]string{
	".go":   "go",
	".cs":   "dotnet",
	".csx":  "dotnet",
	".py":   "python",
	".ts":   "node",
	".js":   "node",
	".tsx":  "node",
	".jsx":  "node",
	".rs":   "rust",
	".java": "java",
	".kt":   "kotlin",
	".yaml": "",
	".yml":  "",
	".json": "",
	".md":   "",
}

func (d *Detector) scoreExtensionHints(fileName string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(fileName))
	language, known := extensionToLanguage[ext]
	if !known || language == "" {
		return "", false
	}
	var matches []string
	for projectID := range d.registeredProjects {
		for _, kw := range d.registeredProjects[projectID] {
			if strings.ToLower(kw) == language {
				matches = append(matches, projectID)
				break
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return "", false
}

// ── HELPERS ──────────────────────────────────────────────────────────────────

func extractAnnotation(line string, key string) (string, bool) {
	line = strings.TrimPrefix(line, "//")
	line = strings.TrimPrefix(line, "#")
	line = strings.TrimPrefix(line, "*")
	line = strings.TrimSpace(line)
	idx := strings.Index(line, key)
	if idx == -1 {
		return "", false
	}
	value := strings.TrimSpace(line[idx+len(key):])
	if value == "" {
		return "", false
	}
	return value, true
}

func appendMethod(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "+" + addition
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func formatPct(f float64) string {
	return fmt.Sprintf("%.0f%%", f*100)
}
