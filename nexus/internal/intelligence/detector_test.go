// @nexus-project: nexus
// @nexus-path: internal/intelligence/detector_test.go
package intelligence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── HELPERS ───────────────────────────────────────────────────────────────────

// tempFile creates a temporary file with the given name and content.
// Returned path is cleaned up automatically by t.Cleanup.
func tempFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	return path
}

// ── FILENAME PREFIX LAYER ─────────────────────────────────────────────────────

func TestDetect_FilenamePrefix(t *testing.T) {
	detector := NewDetector(map[string][]string{
		"nexus": {"package main", "nexus"},
		"ums":   {"university", "student"},
	})

	tests := []struct {
		name            string
		filename        string
		wantProjectID   string
		wantMinConf     float64
		wantMethodPart  string
	}{
		{
			name:           "nexus prefix detected",
			filename:       "nexus__eventbus__20260312_1500.go",
			wantProjectID:  "nexus",
			wantMinConf:    0.40,
			wantMethodPart: "prefix",
		},
		{
			name:           "ums prefix detected",
			filename:       "ums__identity_fix__20260312.cs",
			wantProjectID:  "ums",
			wantMinConf:    0.40,
			wantMethodPart: "prefix",
		},
		{
			name:           "unknown prefix returns zero conf",
			filename:       "random_report.pdf",
			wantProjectID:  "",
			wantMinConf:    0.0,
			wantMethodPart: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := tempFile(t, tc.filename, "")
			result := detector.Detect(path)

			if result.ProjectID != tc.wantProjectID {
				t.Errorf("ProjectID = %q, want %q", result.ProjectID, tc.wantProjectID)
			}
			if result.Confidence < tc.wantMinConf {
				t.Errorf("Confidence = %.2f, want >= %.2f", result.Confidence, tc.wantMinConf)
			}
			if tc.wantMethodPart != "" && !strings.Contains(result.Method, tc.wantMethodPart) {
				t.Errorf("Method = %q, want to contain %q", result.Method, tc.wantMethodPart)
			}
		})
	}
}

// ── HEADER COMMENT LAYER ──────────────────────────────────────────────────────

func TestDetect_HeaderAnnotation(t *testing.T) {
	detector := NewDetector(map[string][]string{
		"nexus": {"package daemon"},
	})

	tests := []struct {
		name           string
		filename       string
		content        string
		wantProjectID  string
		wantTargetPath string
		wantMinConf    float64
	}{
		{
			name:     "nexus-project annotation",
			filename: "some_drop.go",
			content: `// @nexus-project: nexus
// @nexus-path: internal/daemon/engine.go
package daemon
`,
			wantProjectID:  "nexus",
			wantTargetPath: "internal/daemon/engine.go",
			wantMinConf:    0.70,
		},
		{
			name:     "no annotation — no boost from this layer",
			filename: "plain.go",
			content:  "package main\nfunc main() {}\n",
			wantProjectID: "",
			wantMinConf:   0.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := tempFile(t, tc.filename, tc.content)
			result := detector.Detect(path)

			if result.ProjectID != tc.wantProjectID {
				t.Errorf("ProjectID = %q, want %q", result.ProjectID, tc.wantProjectID)
			}
			if tc.wantTargetPath != "" && result.TargetPath != tc.wantTargetPath {
				t.Errorf("TargetPath = %q, want %q", result.TargetPath, tc.wantTargetPath)
			}
			if result.Confidence < tc.wantMinConf {
				t.Errorf("Confidence = %.2f, want >= %.2f", result.Confidence, tc.wantMinConf)
			}
		})
	}
}

// ── CONTENT SCAN LAYER ────────────────────────────────────────────────────────

func TestDetect_ContentKeywords(t *testing.T) {
	detector := NewDetector(map[string][]string{
		"ums": {"university", "student", "enrollment"},
	})

	t.Run("content keywords boost confidence", func(t *testing.T) {
		content := `
// This file handles student enrollment in the University Management System.
// university: enrollment logic below.
func enroll(studentID string) {}
`
		path := tempFile(t, "enrollment_handler.go", content)
		result := detector.Detect(path)

		if result.ProjectID != "ums" {
			t.Errorf("ProjectID = %q, want %q", result.ProjectID, "ums")
		}
		if result.Confidence < 0.20 {
			t.Errorf("Confidence = %.2f, want >= 0.20", result.Confidence)
		}
	})
}

// ── EXTENSION HINTS LAYER ─────────────────────────────────────────────────────

func TestDetect_ExtensionHints(t *testing.T) {
	detector := NewDetector(nil)

	tests := []struct {
		filename       string
		wantSignalPart string
	}{
		{"main.go", "go"},
		{"Program.cs", "csharp"},
		{"app.py", "python"},
		{"server.ts", "typescript"},
		{"report.pdf", "document"},
	}

	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			path := tempFile(t, tc.filename, "")
			result := detector.Detect(path)
			// Extension layer contributes to Signals even when project is unknown.
			found := false
			for _, s := range result.Signals {
				if strings.Contains(strings.ToLower(s), tc.wantSignalPart) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected signal containing %q in %v", tc.wantSignalPart, result.Signals)
			}
		})
	}
}

// ── CONFIDENCE CAP ────────────────────────────────────────────────────────────

func TestDetect_ConfidenceNeverExceedsOne(t *testing.T) {
	// Fire all 4 layers at once — confidence must cap at 1.0.
	detector := NewDetector(map[string][]string{
		"nexus": {"package daemon", "engine", "reconcile"},
	})

	content := `// @nexus-project: nexus
// @nexus-path: internal/daemon/engine.go
// package daemon — reconcile engine
package daemon

func reconcile() {} // engine reconcile
`
	path := tempFile(t, "nexus__engine__20260312.go", content)
	result := detector.Detect(path)

	if result.Confidence > 1.0 {
		t.Errorf("Confidence = %.4f, must never exceed 1.0", result.Confidence)
	}
}

// ── NONEXISTENT FILE ─────────────────────────────────────────────────────────

func TestDetect_NonexistentFile(t *testing.T) {
	detector := NewDetector(nil)
	result := detector.Detect("/tmp/nexus_test_file_does_not_exist_abc123.go")

	// Should not panic — returns zero-confidence result.
	if result.Confidence != 0.0 {
		t.Errorf("expected 0.0 confidence for nonexistent file, got %.2f", result.Confidence)
	}
}
