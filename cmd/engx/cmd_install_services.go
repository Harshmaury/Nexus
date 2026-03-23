// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_install_services.go
// Platform service binary downloader (LAUNCH-FIX-4).
//
// engx platform install now:
//   1. Registers engxd as a system service (existing)
//   2. Downloads platform service binaries to ~/bin/ (new)
//
// Binary naming: <svc>-<version>-<os>-<arch>.tar.gz
// from https://github.com/Harshmaury/<Repo>/releases/latest
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// platformService describes one engx platform service binary.
type platformService struct {
	ID   string // "atlas", "forge", etc.
	Repo string // GitHub repo: "Atlas", "Forge", etc.
}

// platformServiceRegistry is the download manifest for engx platform install.
var platformServiceRegistry = []platformService{
	{ID: "atlas",     Repo: "Atlas"},
	{ID: "forge",     Repo: "Forge"},
	{ID: "metrics",   Repo: "Metrics"},
	{ID: "navigator", Repo: "Navigator"},
	{ID: "guardian",  Repo: "Guardian"},
	{ID: "observer",  Repo: "Observer"},
	{ID: "sentinel",  Repo: "Sentinel"},
}

// downloadPlatformServices downloads all platform service binaries to installDir.
// Idempotent — skips binaries that already exist.
func downloadPlatformServices(installDir string) error {
	if runtime.GOOS == "windows" {
		fmt.Println("  ! Service binaries not available for Windows — use WSL2")
		return nil
	}

	os_ := runtime.GOOS
	arch := runtime.GOARCH

	if err := os.MkdirAll(installDir, 0755); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	skipped := 0

	for _, svc := range platformServiceRegistry {
		binPath := filepath.Join(installDir, svc.ID)
		if _, err := os.Stat(binPath); err == nil {
			skipped++
			continue
		}

		fmt.Printf("  → %-12s resolving version...\n", svc.ID)

		version, err := resolveLatestVersion(client, svc.Repo)
		if err != nil {
			fmt.Printf("  ! %-12s version check failed: %v\n", svc.ID, err)
			continue
		}

		tarURL := fmt.Sprintf(
			"https://github.com/Harshmaury/%s/releases/download/v%s/%s-%s-%s-%s.tar.gz",
			svc.Repo, version, svc.ID, version, os_, arch,
		)

		fmt.Printf("  → %-12s downloading v%s...\n", svc.ID, version)

		if err := downloadAndExtract(client, tarURL, "bin/"+svc.ID, binPath); err != nil {
			fmt.Printf("  ! %-12s failed: %v\n", svc.ID, err)
			continue
		}

		fmt.Printf("  ✓ %-12s v%s\n", svc.ID, version)
	}

	if skipped > 0 {
		fmt.Printf("  ✓ %d service(s) already installed\n", skipped)
	}
	return nil
}

// resolveLatestVersion returns the latest release version string (without "v").
func resolveLatestVersion(client *http.Client, repo string) (string, error) {
	url := "https://api.github.com/repos/Harshmaury/" + repo + "/releases/latest"
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "engx-platform-install/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	tag := rel.TagName
	if len(tag) > 0 && tag[0] == 'v' {
		tag = tag[1:]
	}
	if tag == "" {
		return "", fmt.Errorf("no release found")
	}
	return tag, nil
}

// downloadAndExtract downloads a tar.gz and extracts one binary from it.
func downloadAndExtract(client *http.Client, tarURL, innerPath, destPath string) error {
	resp, err := client.Get(tarURL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, tarURL)
	}

	// Write tarball to temp file
	tmp, err := os.CreateTemp("", "engx-svc-*.tar.gz")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("write tarball: %w", err)
	}
	tmp.Close()

	// Extract the binary using system tar
	destTmp := destPath + ".tmp"
	defer os.Remove(destTmp)

	out, err := os.Create(destTmp)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}

	cmd := exec.Command("tar", "-xzf", tmp.Name(), "-O", innerPath)
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		out.Close()
		return fmt.Errorf("extract %s: %w", innerPath, err)
	}
	out.Close()

	if err := os.Chmod(destTmp, 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	return os.Rename(destTmp, destPath)
}
