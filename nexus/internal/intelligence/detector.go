// @nexus-project: nexus
// @nexus-path: internal/intelligence/detector.go
// Package intelligence contains the Nexus Drop detection and routing pipeline.
// Detector runs up to 4 independent scoring layers against a file,
// accumulates a weighted confidence score, and returns a DetectionResult.
// It has no side effects — pure scoring only.
package intelligence

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	// Layer weights — must sum to ≤ 1.2 (capped to 1.0 at output).
	weightFilenamePrefix = 0.50
	weightHeaderComment  = 0.40
	weightContentScan    = 0.20
	weightExtensionHints = 0.10

	// Short-circuit threshold — skip remaining layers if already certain.
	certaintyThreshold = 1.0

	// Max lines to scan for header comments and content keywords.
	maxHeaderLines  = 5
	maxContentLines = 100
)

// ── DETECTION RESULT ─────────────────────────────────────────────────────────

// DetectionResult is the output of running all layers against a file.
type DetectionResult struct {
	FilePath   string
	FileName   string
	ProjectID  string  // detected project, empty if unknown
	TargetPath string  // detected target path within project, empty if unknown
	Confidence float64 // 0.0 to 1.0
	Method     string  // which layer(s) contributed
	Signals    []string // human-readable explanation of each signal
}

// ── DETECTOR ─────────────────────────────────────────────────────────────────

// Detector scores files using a weighted multi-layer model.
// Each layer is independent — no layer depends on another's output.
type Detector struct {
	// registeredProjects maps project ID to its keyword list.
	// Populated from the state store at startup.
	registeredProjects map[string][]string
}

// NewDetector creates a Detector with a known project registry.
// projectKeywords: map[projectID][]keywords
func NewDetector(projectKeywords map[string][]string) *Detector {
	return &Detector{
		registeredProjects: projectKeywords,
	}
}

// ── DETECT ───────────────────────────────────────────────────────────────────

// Detect runs all scoring layers against a file and returns the result.
// Layers run in order of weight (highest first) and short-circuit at certainty.
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
			result.Confidence = min(result.Confidence, 1.0)
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
			result.Confidence = min(result.Confidence, 1.0)
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

	result.Confidence = min(result.Confidence, 1.0)
	return result
}

// ── LAYER 1: FILENAME PREFIX ─────────────────────────────────────────────────

// scoreFilenamePrefix checks for [project]__[feature]__[timestamp].[ext] format.
// Returns projectID, targetPath hint, and whether it matched.
func (d *Detector) scoreFilenamePrefix(fileName string) (string, string, bool) {
	// Strip extension
	name := strings.TrimSuffix(fileName, filepath.Ext(fileName))

	parts := strings.SplitN(name, "__", 3)
	if len(parts) < 2 {
		return "", "", false
	}

	projectID := strings.ToLower(strings.TrimSpace(parts[0]))
	if projectID == "" {
		return "", "", false
	}

	// Verify project is registered.
	if _, known := d.registeredProjects[projectID]; !known {
		return "", "", false
	}

	return projectID, "", true
}

// ── LAYER 2: HEADER COMMENT ──────────────────────────────────────────────────

// scoreHeaderComment scans the first N lines for @nexus-project and @nexus-path.
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

	// Verify project is registered.
	if _, known := d.registeredProjects[projectID]; !known {
		return "", "", false
	}

	return projectID, targetPath, true
}

// ── LAYER 3: CONTENT SCAN ────────────────────────────────────────────────────

// scoreContentScan scans file content for registered project keywords.
func (d *Detector) scoreContentScan(filePath string) (string, bool) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", false
	}
	defer file.Close()

	// Collect first N lines as a single lowercase string for scanning.
	var sb strings.Builder
	scanner := bufio.NewScanner(file)
	lineCount := 0

	for scanner.Scan() && lineCount < maxContentLines {
		sb.WriteString(strings.ToLower(scanner.Text()))
		sb.WriteString("\n")
		lineCount++
	}

	content := sb.String()

	// Check each project's keywords — first match wins.
	for projectID, keywords := range d.registeredProjects {
		matchCount := 0
		for _, keyword := range keywords {
			if strings.Contains(content, strings.ToLower(keyword)) {
				matchCount++
			}
		}
		// Require at least 2 keyword matches to avoid false positives.
		if matchCount >= 2 {
			return projectID, true
		}
	}

	return "", false
}

// ── LAYER 4: EXTENSION HINTS ─────────────────────────────────────────────────

// extensionToLanguage maps file extensions to language names.
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

// scoreExtensionHints maps file extension to a language and checks if any
// registered project uses that language.
func (d *Detector) scoreExtensionHints(fileName string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(fileName))
	language, known := extensionToLanguage[ext]
	if !known || language == "" {
		return "", false
	}

	// Find a registered project matching this language.
	// If multiple match, we cannot distinguish — return no match.
	matches := []string{}
	for projectID := range d.registeredProjects {
		// Project keywords include language as first keyword by convention.
		keywords := d.registeredProjects[projectID]
		for _, kw := range keywords {
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

// extractAnnotation finds "// @key: value" or "# @key: value" patterns.
func extractAnnotation(line string, key string) (string, bool) {
	// Strip comment markers
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

func appendMethod(existing string, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "+" + addition
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
