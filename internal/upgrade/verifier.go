// @nexus-project: nexus
// @nexus-path: internal/upgrade/verifier.go
// Checksum verification for the engx upgrade protocol (ADR-028).
// Downloads the goreleaser checksums manifest and verifies the tarball SHA256.
package upgrade

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// VerifyChecksum downloads the checksums manifest from checksumURL and
// verifies that the file at tarballPath matches the expected SHA256.
// filename is the base name used to find the correct line in the manifest.
func VerifyChecksum(ctx context.Context, checksumURL, filename, tarballPath string) error {
	expected, err := fetchExpectedHash(ctx, checksumURL, filename)
	if err != nil {
		return fmt.Errorf("fetch checksum manifest: %w", err)
	}
	actual, err := sha256File(tarballPath)
	if err != nil {
		return fmt.Errorf("compute sha256: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("checksum mismatch\n  expected: %s\n  actual:   %s", expected, actual)
	}
	return nil
}

// fetchExpectedHash downloads the checksums file and extracts the hash for filename.
// The goreleaser checksums format is: "<sha256>  <filename>" one entry per line.
func fetchExpectedHash(ctx context.Context, checksumURL, filename string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksums download: HTTP %d", resp.StatusCode)
	}
	return parseChecksumLine(resp.Body, filename)
}

// parseChecksumLine scans the checksums manifest for the line matching filename.
func parseChecksumLine(r io.Reader, filename string) (string, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		// goreleaser writes "  <filename>" (two spaces) but Fields splits on any whitespace.
		if filepath.Base(parts[1]) == filepath.Base(filename) {
			return parts[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan checksums: %w", err)
	}
	return "", fmt.Errorf("no checksum found for %q in manifest", filename)
}

// sha256File computes the hex-encoded SHA256 digest of a file.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
