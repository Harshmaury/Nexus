// @nexus-project: nexus
// @nexus-path: internal/intelligence/renamer.go
// Renamer ensures files dropped into nexus-drop follow the
// [project]__[feature]__[YYYYMMDD_HHMM].[ext] naming convention.
// If the file already follows the convention it is left unchanged.
// Renamer has no side effects beyond os.Rename — pure naming logic.
package intelligence

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

// nexusNamePattern matches [project]__[feature]__[YYYYMMDD_HHMM]
var nexusNamePattern = regexp.MustCompile(`^[a-z0-9\-]+__[a-z0-9_\-]+__\d{8}_\d{4}`)

// ── RENAME RESULT ─────────────────────────────────────────────────────────────

// RenameResult is the outcome of a rename operation.
type RenameResult struct {
	OriginalPath string
	FinalPath    string
	OriginalName string
	FinalName    string
	WasRenamed   bool // false if already correct format
}

// ── RENAMER ───────────────────────────────────────────────────────────────────

// Renamer normalises file names to the Nexus drop convention.
type Renamer struct{}

// NewRenamer creates a Renamer.
func NewRenamer() *Renamer {
	return &Renamer{}
}

// ── RENAME ───────────────────────────────────────────────────────────────────

// Rename checks if a file needs renaming and applies it if so.
// If the file already follows the convention it is returned unchanged.
func (r *Renamer) Rename(filePath string, projectID string, feature string) (RenameResult, error) {
	dir := filepath.Dir(filePath)
	originalName := filepath.Base(filePath)
	ext := filepath.Ext(originalName)
	nameNoExt := strings.TrimSuffix(originalName, ext)

	result := RenameResult{
		OriginalPath: filePath,
		OriginalName: originalName,
	}

	// Already in correct format — nothing to do.
	if nexusNamePattern.MatchString(nameNoExt) {
		result.FinalPath = filePath
		result.FinalName = originalName
		result.WasRenamed = false
		return result, nil
	}

	// Build canonical name.
	timestamp := time.Now().Format("20060102_1504")
	cleanFeature := sanitiseSegment(feature)
	cleanProject := sanitiseSegment(projectID)

	if cleanFeature == "" {
		cleanFeature = sanitiseSegment(nameNoExt)
	}
	if cleanFeature == "" {
		cleanFeature = "file"
	}

	newName := fmt.Sprintf("%s__%s__%s%s", cleanProject, cleanFeature, timestamp, ext)
	newPath := filepath.Join(dir, newName)

	// Avoid clobbering an existing file.
	newPath = uniquePath(newPath)
	newName = filepath.Base(newPath)

	if err := os.Rename(filePath, newPath); err != nil {
		return result, fmt.Errorf("rename %s → %s: %w", originalName, newName, err)
	}

	result.FinalPath = newPath
	result.FinalName = newName
	result.WasRenamed = true
	return result, nil
}

// IsCanonical returns true if the filename already follows the Nexus convention.
func IsCanonical(fileName string) bool {
	nameNoExt := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	return nexusNamePattern.MatchString(nameNoExt)
}

// ParseCanonicalName extracts projectID and feature from a canonical filename.
// Returns empty strings if the filename does not follow the convention.
func ParseCanonicalName(fileName string) (projectID string, feature string) {
	ext := filepath.Ext(fileName)
	nameNoExt := strings.TrimSuffix(fileName, ext)

	parts := strings.SplitN(nameNoExt, "__", 3)
	if len(parts) < 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// ── HELPERS ──────────────────────────────────────────────────────────────────

// sanitiseSegment lowercases and replaces illegal characters with hyphens.
func sanitiseSegment(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = regexp.MustCompile(`[^a-z0-9\-_]`).ReplaceAllString(s, "-")
	s = regexp.MustCompile(`-{2,}`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// uniquePath appends a counter suffix if the path already exists.
func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}

	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)

	for i := 1; i <= 99; i++ {
		candidate := fmt.Sprintf("%s_%02d%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}

	// Fallback — append nanoseconds (will never collide).
	return fmt.Sprintf("%s_%d%s", base, time.Now().UnixNano(), ext)
}
