// @nexus-project: nexus
// @nexus-path: internal/upgrade/installer.go
// Download, extract, preflight, and atomic-swap logic for engx upgrade (ADR-028).
package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// downloadTimeout is the maximum time allowed to download the tarball.
	downloadTimeout = 5 * time.Minute
	// preflightTimeout is the maximum time allowed for the new binary's doctor check.
	preflightTimeout = 30 * time.Second
	// Binaries is the set of executables extracted and swapped on upgrade.
)

// Binaries is the ordered list of executables in the release tarball.
var Binaries = []string{"engxd", "engx", "engxa"}

// Download streams the tarball at url to destPath, respecting ctx deadline.
func Download(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download tarball: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write tarball: %w", err)
	}
	return nil
}

// Extract unpacks the named binaries from a .tar.gz archive into destDir.
// Only files whose base name matches an entry in binaries are extracted.
func Extract(tarPath, destDir string, binaries []string) error {
	want := make(map[string]bool, len(binaries))
	for _, b := range binaries {
		want[b] = true
	}
	f, err := os.Open(tarPath)
	if err != nil {
		return fmt.Errorf("open tarball: %w", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()
	return extractBinaries(tar.NewReader(gr), destDir, want)
}

// extractBinaries iterates a tar archive and writes matching entries to destDir.
func extractBinaries(tr *tar.Reader, destDir string, want map[string]bool) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		base := filepath.Base(hdr.Name)
		if !want[base] || hdr.Typeflag != tar.TypeReg {
			continue
		}
		if err := writeExecutable(tr, filepath.Join(destDir, base)); err != nil {
			return fmt.Errorf("extract %s: %w", base, err)
		}
	}
	return nil
}

// writeExecutable writes a tar entry to destPath with executable permissions.
func writeExecutable(r io.Reader, destPath string) error {
	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// Preflight runs `<newEngxPath> doctor` against the live engxd.
// The new binary must exit 0; any non-zero exit aborts the upgrade (ADR-028 Rule 3).
func Preflight(ctx context.Context, newEngxPath, httpAddr string) error {
	pfCtx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	cmd := exec.CommandContext(pfCtx, newEngxPath, "doctor", "--http", httpAddr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("preflight doctor check failed: %w\n  new binary: %s", err, newEngxPath)
	}
	return nil
}

// AtomicSwap replaces each binary in binDir with the new version from srcDir.
// Uses os.Rename for atomicity (ADR-028 Rule 4). srcDir and binDir must be on
// the same filesystem — both are under ~ so this is always satisfied.
func AtomicSwap(srcDir, binDir string, binaries []string) error {
	for _, name := range binaries {
		src := filepath.Join(srcDir, name)
		dst := filepath.Join(binDir, name)
		if err := swapBinary(src, dst); err != nil {
			return fmt.Errorf("swap %s: %w", name, err)
		}
	}
	return nil
}

// swapBinary atomically replaces dst with src.
// Backs up the existing binary to dst.bak before renaming, so a failed
// subsequent rename leaves the original in place.
func swapBinary(src, dst string) error {
	if err := ensureParentDir(dst); err != nil {
		return err
	}
	backup := dst + ".bak"
	if fileExists(src) && fileExists(dst) {
		if err := os.Rename(dst, backup); err != nil {
			return fmt.Errorf("backup existing binary: %w", err)
		}
	}
	if err := os.Rename(src, dst); err != nil {
		// Attempt to restore the backup so the system is not left without a binary.
		_ = os.Rename(backup, dst)
		return fmt.Errorf("rename new binary into place: %w", err)
	}
	_ = os.Remove(backup) // cleanup; ignore errors
	return nil
}

// ensureParentDir creates the parent directory of path if it does not exist.
func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create bin dir %s: %w", dir, err)
	}
	return nil
}

// BinDir returns the canonical binary installation directory (~/.bin or ~/bin).
// Matches the directory used by engx platform install (ADR-026 / ADR-028).
func BinDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, "bin"), nil
}

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// TarballFilename returns the base filename for the current platform's tarball.
func TarballFilename(version string) string {
	return fmt.Sprintf("engx-%s-%s-%s.tar.gz",
		version, goOS(), goArch())
}

// goOS returns the GOOS value for asset URL construction.
func goOS() string {
	return strings.ToLower(osName())
}

// goArch returns the GOARCH value for asset URL construction.
func goArch() string {
	return archName()
}
