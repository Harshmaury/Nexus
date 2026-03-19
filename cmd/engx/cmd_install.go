// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_install.go
// Platform install/uninstall commands — Nexus Phase 18 (ADR-026).
//
// engx platform install   — registers engxd as a user-space system service
// engx platform uninstall — removes the service registration
// engx platform logs      — tails the engxd system service log
//
// Supported init systems:
//   macOS  : launchd  (~/.launchd/dev.engx.daemon.plist)
//   Linux  : systemd  (~/.config/systemd/user/engxd.service)
//   Other  : prints a manual start command and exits cleanly
//
// After install, engxd starts automatically at login.
// Users never need to run "engxd &" again.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
)

// ── CONSTANTS ──────────────────────────────────────────────────────────────────

const (
	launchAgentLabel = "dev.engx.daemon"
	launchAgentPlist = "dev.engx.daemon.plist"
	systemdUnitName  = "engxd.service"
)

// ── PLATFORM INSTALL COMMAND ──────────────────────────────────────────────────

// platformInstallCmd registers engxd as a login-time user service.
func platformInstallCmd() *cobra.Command {
	var binPath string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Register engxd as a system service (starts automatically at login)",
		Long: `Installs engxd as a user-space daemon that starts automatically when
you log in. After running this command you no longer need to run 'engxd &'.

  macOS : installs a launchd LaunchAgent in ~/Library/LaunchAgents/
  Linux : installs a systemd user service in ~/.config/systemd/user/

To remove the service: engx platform uninstall`,
		RunE: func(cmd *cobra.Command, args []string) error {
			bin, err := resolveEngxdBin(binPath)
			if err != nil {
				return err
			}
			return installService(bin)
		},
	}
	cmd.Flags().StringVar(&binPath, "bin", "",
		"path to engxd binary (default: auto-detected from PATH and ~/bin/engxd)")
	return cmd
}

// platformUninstallCmd removes the engxd system service registration.
func platformUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the engxd system service registration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return uninstallService()
		},
	}
}

// platformServiceLogsCmd tails the engxd log file.
func platformServiceLogsCmd() *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show engxd daemon log",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("home dir: %w", err)
			}
			logPath := filepath.Join(home, ".nexus", "logs", "engxd.log")
			return printLogTail(logPath, lines)
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "number of lines to show")
	return cmd
}

// ── INSTALL LOGIC ─────────────────────────────────────────────────────────────

// installService dispatches to the appropriate init system.
func installService(binPath string) error {
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(binPath)
	case "linux":
		return installSystemd(binPath)
	default:
		printManualInstructions(binPath)
		return nil
	}
}

// uninstallService dispatches to the appropriate init system.
func uninstallService() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchd()
	case "linux":
		return uninstallSystemd()
	default:
		fmt.Println("No service manager detected — remove the service manually.")
		return nil
	}
}

// ── MACOS: LAUNCHD ────────────────────────────────────────────────────────────

// launchdPlist returns the plist XML content for the LaunchAgent.
func launchdPlist(binPath, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>

  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
  </array>

  <key>RunAtLoad</key>
  <true/>

  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>

  <key>StandardOutPath</key>
  <string>%s</string>

  <key>StandardErrorPath</key>
  <string>%s</string>

  <key>ProcessType</key>
  <string>Background</string>
</dict>
</plist>
`, launchAgentLabel, binPath, logPath, logPath)
}

// installLaunchd writes a LaunchAgent plist and loads it immediately.
func installLaunchd(binPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}

	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	logDir := filepath.Join(home, ".nexus", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	plistPath := filepath.Join(launchAgentsDir, launchAgentPlist)
	logPath := filepath.Join(logDir, "engxd.log")
	content := launchdPlist(binPath, logPath)

	if err := os.WriteFile(plistPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	fmt.Printf("✓ LaunchAgent written: %s\n", plistPath)

	if err := runOSCmd("launchctl", "load", "-w", plistPath); err != nil {
		fmt.Printf("⚠ launchctl load failed: %v\n", err)
		fmt.Printf("  Load manually: launchctl load -w %s\n", plistPath)
		return nil
	}
	fmt.Println("✓ engxd loaded — will start automatically at login")
	fmt.Printf("  Log: %s\n", logPath)
	fmt.Printf("  Stop: launchctl unload %s\n", plistPath)
	return nil
}

// uninstallLaunchd unloads and removes the LaunchAgent plist.
func uninstallLaunchd() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", launchAgentPlist)

	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		fmt.Println("No LaunchAgent found — nothing to uninstall.")
		return nil
	}

	_ = runOSCmd("launchctl", "unload", "-w", plistPath)
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}
	fmt.Printf("✓ LaunchAgent removed: %s\n", plistPath)
	fmt.Println("  engxd will no longer start at login.")
	return nil
}

// ── LINUX: SYSTEMD ────────────────────────────────────────────────────────────

// systemdUnit returns the systemd unit file content.
func systemdUnit(binPath, logPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Nexus Developer Control Plane
After=network.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5s
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, binPath, logPath, logPath)
}

// installSystemd writes a systemd user service unit and enables it.
func installSystemd(binPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}

	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}

	logPath, err := ensureEngxdLogDir(home)
	if err != nil {
		return err
	}

	unitPath := filepath.Join(unitDir, systemdUnitName)
	if err := os.WriteFile(unitPath, []byte(systemdUnit(binPath, logPath)), 0644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}
	fmt.Printf("✓ systemd unit written: %s\n", unitPath)

	_ = runOSCmd("systemctl", "--user", "daemon-reload")
	if err := runOSCmd("systemctl", "--user", "enable", "--now", systemdUnitName); err != nil {
		fmt.Printf("⚠ systemctl enable failed: %v\n", err)
		fmt.Println("  Enable manually:")
		fmt.Println("    systemctl --user daemon-reload")
		fmt.Printf("    systemctl --user enable --now %s\n", systemdUnitName)
		return nil
	}
	fmt.Println("✓ engxd enabled — will start automatically at login")
	fmt.Printf("  Status: systemctl --user status %s\n", systemdUnitName)
	fmt.Printf("  Log:    %s\n", logPath)
	return nil
}

// ensureEngxdLogDir creates ~/.nexus/logs/ and returns the engxd log path.
func ensureEngxdLogDir(home string) (string, error) {
	logDir := filepath.Join(home, ".nexus", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return "", fmt.Errorf("create log dir: %w", err)
	}
	return filepath.Join(logDir, "engxd.log"), nil
}

// uninstallSystemd disables and removes the systemd user unit.
func uninstallSystemd() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}
	unitPath := filepath.Join(home, ".config", "systemd", "user", systemdUnitName)

	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		fmt.Println("No systemd user service found — nothing to uninstall.")
		return nil
	}

	_ = runOSCmd("systemctl", "--user", "disable", "--now", systemdUnitName)
	if err := os.Remove(unitPath); err != nil {
		return fmt.Errorf("remove unit file: %w", err)
	}
	_ = runOSCmd("systemctl", "--user", "daemon-reload")

	fmt.Printf("✓ systemd service removed: %s\n", unitPath)
	fmt.Println("  engxd will no longer start at login.")
	return nil
}

// ── HELPERS ───────────────────────────────────────────────────────────────────

// resolveEngxdBin finds the engxd binary: --bin flag, ~/bin/engxd, or PATH.
func resolveEngxdBin(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	// Prefer ~/bin/engxd (the canonical install location for this project)
	localBin := filepath.Join(home, "bin", "engxd")
	if _, err := os.Stat(localBin); err == nil {
		return localBin, nil
	}
	// Fall back to PATH
	if path, err := exec.LookPath("engxd"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf(
		"engxd not found — build it first:\n  go install ./cmd/engxd/ && cp ~/go/bin/engxd ~/bin/engxd\n  then re-run: engx platform install")
}

// runCmd executes a command and returns any non-zero exit error.
func runOSCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...) //nolint:gosec
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// printManualInstructions prints a manual start guide for unsupported platforms.
func printManualInstructions(binPath string) {
	fmt.Printf("Unsupported platform (%s). To start engxd manually:\n\n", runtime.GOOS)
	fmt.Printf("  %s &\n\n", binPath)
	fmt.Println("Add this line to your shell profile (~/.bashrc, ~/.zshrc) to start at login.")
}
