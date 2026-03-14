// @nexus-project: nexus
// @nexus-path: internal/intelligence/notifier.go
// Notifier abstracts OS-level desktop notifications from the drop router.
// Previously the router called powershell.exe directly — which silently
// fails in WSL Ubuntu (the primary development environment) and violates
// the environment-agnostic constraint.
//
// Implementations:
//   LinuxNotifier  — uses notify-send if available, falls back to terminal
//   NullNotifier   — no-op, used in tests and environments without a display
//
// NewDefaultNotifier detects the runtime environment and returns the
// appropriate implementation. The router never knows which one it has.
package intelligence

import (
	"fmt"
	"os/exec"
	"path/filepath"
)

// ── INTERFACE ─────────────────────────────────────────────────────────────────

// Notifier sends a desktop notification to the user.
// Implementations must be safe for concurrent use.
type Notifier interface {
	// Notify sends a notification with the given title and message.
	// Errors are logged internally — callers do not need to handle failures.
	Notify(title, message string)
}

// ── LINUX NOTIFIER ────────────────────────────────────────────────────────────

// LinuxNotifier sends notifications via notify-send (freedesktop standard).
// If notify-send is unavailable, it falls back to printing to the terminal.
// This is the correct implementation for WSL Ubuntu and native Linux.
type LinuxNotifier struct {
	notifySendPath string // empty = not found, use terminal fallback
}

// NewLinuxNotifier creates a LinuxNotifier.
// It checks for notify-send at construction time so the hot path avoids exec.LookPath.
func NewLinuxNotifier() *LinuxNotifier {
	path, err := exec.LookPath("notify-send")
	if err != nil {
		path = ""
	}
	return &LinuxNotifier{notifySendPath: path}
}

// Notify sends a desktop notification or falls back to terminal output.
func (n *LinuxNotifier) Notify(title, message string) {
	if n.notifySendPath != "" {
		cmd := exec.Command(n.notifySendPath,
			"--app-name=Nexus",
			"--urgency=normal",
			title,
			message,
		)
		if err := cmd.Run(); err == nil {
			return
		}
		// notify-send found but failed (e.g. no DISPLAY in SSH session) — fall through to terminal
	}
	// Terminal fallback: always works regardless of environment
	fmt.Printf("\n\033[36m[NEXUS]\033[0m %s\n  %s\n\n", title, message)
}

// ── NULL NOTIFIER ─────────────────────────────────────────────────────────────

// NullNotifier silently discards all notifications.
// Use in tests, CI, and headless environments where no display is available.
type NullNotifier struct{}

// Notify is a no-op.
func (n *NullNotifier) Notify(title, message string) {}

// ── FACTORY ───────────────────────────────────────────────────────────────────

// NewDefaultNotifier returns the best Notifier for the current environment.
//
// Detection logic:
//   - If notify-send is on PATH → LinuxNotifier with notify-send backend
//   - Otherwise               → LinuxNotifier with terminal fallback
//
// The old Windows-specific code (powershell.exe / WinRT toast) is not present
// here because the primary environment is WSL Ubuntu. When Nexus gains native
// Windows support, a WindowsNotifier can be added and selected here.
func NewDefaultNotifier() Notifier {
	return NewLinuxNotifier()
}

// notifySendAvailable reports whether notify-send is on the PATH.
// Used by tests to skip notification-dependent assertions in CI.
func notifySendAvailable() bool {
	_, err := exec.LookPath("notify-send")
	return err == nil
}

// notifierName returns the binary name from a path for logging.
func notifierName(path string) string {
	if path == "" {
		return "terminal"
	}
	return filepath.Base(path)
}
