// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_upgrade.go
// engx upgrade — self-update command for the Nexus developer platform (ADR-028).
//
// Protocol:
//   1. Resolve latest release from GitHub Releases API
//   2. Build platform-specific asset URLs (goreleaser naming convention)
//   3. Download tarball to temp file
//   4. Verify SHA256 against the signed checksums manifest
//   5. Extract engxd, engx, engxa to a temp directory
//   6. Preflight: run new engx doctor against the live engxd (must exit 0)
//   7. Atomic swap: os.Rename each binary to ~/bin/
//
// Flags:
//   --dry-run       print plan and run preflight, but do not swap binaries
//   --channel beta  target the latest pre-release instead of stable
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Harshmaury/Nexus/internal/upgrade"
	"github.com/spf13/cobra"
)

const (
	upgradeDownloadTimeout = 5 * time.Minute
	upgradeStepTimeout     = 2 * time.Minute
)

// upgradeCmd returns the cobra command for engx upgrade.
func upgradeCmd(httpAddr *string) *cobra.Command {
	var dryRun bool
	var channel string

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade engx, engxd, and engxa to the latest release",
		Long: `Downloads the latest release from GitHub, verifies the SHA256 checksum,
runs 'engx doctor' against your live platform to confirm the new binary is healthy,
then atomically swaps all three binaries (engxd, engx, engxa) in ~/bin/.

Use --dry-run to see what would happen without making any changes.
Use --channel beta to upgrade to the latest pre-release.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !upgrade.IsSupported() {
				return fmt.Errorf(
					"engx upgrade is not supported on %s — download from GitHub Releases",
					platformDescription())
			}
			return runUpgrade(cmd.Context(), *httpAddr, channel, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"plan and preflight the upgrade without swapping binaries")
	cmd.Flags().StringVar(&channel, "channel", upgrade.ChannelStable,
		"release channel: stable or beta")
	return cmd
}

// runUpgrade executes the full upgrade protocol.
func runUpgrade(ctx context.Context, httpAddr, channel string, dryRun bool) error {
	fmt.Printf("engx upgrade  channel=%s  dry-run=%v\n\n", channel, dryRun)

	rel, err := resolveRelease(ctx, channel)
	if err != nil {
		return err
	}
	tmpDir, tarPath, err := downloadAndVerify(ctx, rel)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return err
	}
	defer os.RemoveAll(tmpDir)

	if err := extractBinaries(tarPath, tmpDir); err != nil {
		return err
	}
	if err := runPreflight(ctx, tmpDir, httpAddr); err != nil {
		return err
	}
	return swapOrDryRun(ctx, tmpDir, dryRun, rel.Version)
}

// resolveRelease fetches and prints the release to install.
func resolveRelease(ctx context.Context, channel string) (*upgrade.Release, error) {
	fmt.Printf("  → resolving latest %s release...\n", channel)
	rCtx, cancel := context.WithTimeout(ctx, upgradeStepTimeout)
	defer cancel()
	rel, err := upgrade.FetchLatest(rCtx, channel)
	if err != nil {
		return nil, fmt.Errorf("resolve release: %w", err)
	}
	preTag := ""
	if rel.IsPreRelease {
		preTag = " (pre-release)"
	}
	fmt.Printf("  ✓ found %s%s\n", rel.Tag, preTag)
	fmt.Printf("    tarball: %s\n\n", rel.TarballURL)
	return rel, nil
}

// downloadAndVerify downloads the tarball and verifies its checksum.
// Returns the temp directory and the path to the downloaded tarball.
func downloadAndVerify(ctx context.Context, rel *upgrade.Release) (string, string, error) {
	tmpDir, err := os.MkdirTemp("", "engx-upgrade-*")
	if err != nil {
		return "", "", fmt.Errorf("create temp dir: %w", err)
	}
	filename := upgrade.TarballFilename(rel.Version)
	tarPath := filepath.Join(tmpDir, filename)

	fmt.Printf("  → downloading tarball...\n")
	dlCtx, dlCancel := context.WithTimeout(ctx, upgradeDownloadTimeout)
	defer dlCancel()
	if err := upgrade.Download(dlCtx, rel.TarballURL, tarPath); err != nil {
		return tmpDir, "", fmt.Errorf("download: %w", err)
	}
	fmt.Printf("  ✓ downloaded %s\n", filename)

	fmt.Printf("  → verifying SHA256 checksum...\n")
	vCtx, vCancel := context.WithTimeout(ctx, upgradeStepTimeout)
	defer vCancel()
	if err := upgrade.VerifyChecksum(vCtx, rel.ChecksumsURL, filename, tarPath); err != nil {
		return tmpDir, "", fmt.Errorf("checksum: %w", err)
	}
	fmt.Printf("  ✓ checksum verified\n\n")
	return tmpDir, tarPath, nil
}

// extractBinaries unpacks engxd, engx, engxa into tmpDir.
func extractBinaries(tarPath, tmpDir string) error {
	fmt.Printf("  → extracting binaries...\n")
	if err := upgrade.Extract(tarPath, tmpDir, upgrade.Binaries); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	for _, b := range upgrade.Binaries {
		fmt.Printf("    extracted %s\n", b)
	}
	fmt.Println()
	return nil
}

// runPreflight runs doctor with the new binary against the live engxd.
func runPreflight(ctx context.Context, tmpDir, httpAddr string) error {
	newEngx := filepath.Join(tmpDir, "engx")
	fmt.Printf("  → preflight: running new engx doctor...\n\n")
	pfCtx, cancel := context.WithTimeout(ctx, upgradeStepTimeout)
	defer cancel()
	if err := upgrade.Preflight(pfCtx, newEngx, httpAddr); err != nil {
		return fmt.Errorf("preflight failed — upgrade aborted, no files changed:\n  %w", err)
	}
	fmt.Printf("\n  ✓ preflight passed\n\n")
	return nil
}

// swapOrDryRun either performs the atomic swap or prints the dry-run summary.
func swapOrDryRun(ctx context.Context, tmpDir string, dryRun bool, version string) error {
	binDir, err := upgrade.BinDir()
	if err != nil {
		return err
	}
	if dryRun {
		printDryRunSummary(binDir, version)
		return nil
	}
	fmt.Printf("  → swapping binaries in %s...\n", binDir)
	if err := upgrade.AtomicSwap(tmpDir, binDir, upgrade.Binaries); err != nil {
		return fmt.Errorf("atomic swap: %w", err)
	}
	printUpgradeSuccess(version)
	return nil
}

// printDryRunSummary prints what the swap would have done.
func printDryRunSummary(binDir, version string) {
	fmt.Println("  ○ dry-run: skipping binary swap")
	for _, b := range upgrade.Binaries {
		fmt.Printf("    would write: %s\n", filepath.Join(binDir, b))
	}
	fmt.Printf("\n  To apply: engx upgrade --channel stable\n\n")
}

// printUpgradeSuccess prints the post-upgrade confirmation.
func printUpgradeSuccess(version string) {
	fmt.Printf("  ✓ upgrade complete — now on v%s\n\n", version)
	fmt.Println("  Restart engxd to activate the new daemon:")
	fmt.Println("    engx platform stop && engx platform start")
	fmt.Println()
}

// platformDescription returns a human-readable OS/arch string for error messages.
func platformDescription() string {
	binaries := upgrade.Binaries
	_ = binaries
	if !upgrade.IsSupported() {
		return "this platform (download .zip from https://github.com/Harshmaury/Nexus/releases)"
	}
	return "unknown"
}
