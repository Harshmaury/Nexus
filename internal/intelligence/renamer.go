// @nexus-project: nexus
// @nexus-path: internal/intelligence/renamer.go
// Renamer ensures files dropped into nexus-drop follow the
// [project]__[feature]__[YYYYMMDD_HHMM].[ext] naming convention.
// If the file already follows the convention it is left unchanged.
// Renamer has no side effects beyond os.Rename — pure naming logic.
//
// Fix: sanitiseSegment previously compiled two regexp.MustCompile calls
// on every invocation. Moved to package-level vars — compiled once at startup.
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
var nexusNamePattern = regexp.MustCompile(
	`^[a-z0-9\-_]+__[a-z0-9\-_]+__\d{8}_\d{4}$`,
)

// ── PACKAGE-LEVEL REGEXP (compiled once) ─────────────────────────────────────

// illegalCharsRe replaces any character that is not a-z, 0-9, hyphen, or underscore.
var illegalCharsRe = regexp.MustCompile(`[^a-z0-9\-_]`)

// multiHyphenRe collapses consecutive hyphens into one.
var multiHyphenRe = regexp.MustCompile(`-{2,}`)

// ── RENAMER ───────────────────────────────────────────────────────────────────

// Renamer canonicalises filenames to the Nexus drop convention.
type Renamer struct{}

// NewRenamer creates a Renamer. No state — safe for concurrent use.
func NewRenamer() *Renamer {
	return &Renamer{}
}

// RenameResult is the outcome of one rename operation.
type RenameResult struct {
	OriginalPath string
	NewPath      string
	NewName      string
	WasRenamed   bool // false if the file already followed the convention
}

// Rename renames a file at filePath to the Nexus convention.
// projectID and feature are canonicalised before use.
// If the file already matches the convention, it is left unchanged.
func (r *Renamer) Rename(filePath string, projectID string, feature string) (RenameResult, error) {
	dir := filepath.Dir(filePath)
	ext := filepath.Ext(filePath)
	nameNoExt := strings.TrimSuffix(filepath.Base(filePath), ext)

	cleanFeature := sanitiseSegment(feature)
	cleanProject := sanitiseSegment(projectID)

	// If feature is empty, fall back to sanitised original filename.
	if cleanFeature == "" {
		cleanFeature = sanitiseSegment(nameNoExt)
	}
	if cleanProject == "" {
		cleanProject = "unknown"
	}

	// Already canonical — skip.
	if nexusNamePattern.MatchString(nameNoExt) {
		return RenameResult{
			OriginalPath: filePath,
			NewPath:      filePath,
			NewName:      filepath.Base(filePath),
			WasRenamed:   false,
		}, nil
	}

	timestamp := time.Now().Format("20060102_1504")
	newName := fmt.Sprintf("%s__%s__%s%s", cleanProject, cleanFeature, timestamp, ext)
	newPath := uniquePath(filepath.Join(dir, newName))

	if err := os.Rename(filePath, newPath); err != nil {
		return RenameResult{}, fmt.Errorf("rename %s → %s: %w", filePath, newPath, err)
	}

	return RenameResult{
		OriginalPath: filePath,
		NewPath:      newPath,
		NewName:      filepath.Base(newPath),
		WasRenamed:   true,
	}, nil
}

// ── HELPERS ──────────────────────────────────────────────────────────────────

// sanitiseSegment lowercases and replaces illegal characters with hyphens.
// Uses package-level compiled regexps — safe to call in hot paths.
func sanitiseSegment(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = illegalCharsRe.ReplaceAllString(s, "-")
	s = multiHyphenRe.ReplaceAllString(s, "-")
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

	// Fallback: nanosecond suffix guarantees uniqueness.
	return fmt.Sprintf("%s_%d%s", base, time.Now().UnixNano(), ext)
}
