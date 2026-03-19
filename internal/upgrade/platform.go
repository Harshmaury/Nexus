// @nexus-project: nexus
// @nexus-path: internal/upgrade/platform.go
// OS and architecture name mapping for asset URL construction (ADR-028).
package upgrade

import "runtime"

// osName returns the OS label used in goreleaser asset filenames.
// Returns "" for unsupported platforms (e.g. Windows — ADR-028 Rule 8).
func osName() string {
	switch runtime.GOOS {
	case "darwin":
		return "darwin"
	case "linux":
		return "linux"
	default:
		return ""
	}
}

// archName returns the architecture label used in goreleaser asset filenames.
func archName() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	default:
		return "amd64"
	}
}

// IsSupported reports whether the current platform can use engx upgrade.
// Windows is not supported — users must download the .zip from GitHub Releases.
func IsSupported() bool {
	return osName() != ""
}
