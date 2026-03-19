// @nexus-project: nexus
// @nexus-path: internal/config/env.go
// Package config provides environment variable helpers and path utilities
// shared across cmd/engxd and cmd/engx.
//
// Previously, expandHome, envOrDefault, and durationEnvOrDefault were
// duplicated identically in both cmd/engxd/main.go and cmd/engx/main.go.
// They live here now — imported by both, defined once.
package config

import (
	"os"
	"path/filepath"
	"time"
)

// DefaultDBPath is the default location of the Nexus SQLite state file.
const DefaultDBPath = "~/.nexus/nexus.db"
// DefaultHTTPAddr is the default listen address for the Phase 8 HTTP API.
// Override with the NEXUS_HTTP_ADDR environment variable.
const DefaultHTTPAddr = "127.0.0.1:8080"


// EnvOrDefault returns the value of the environment variable key,
// or fallback if the variable is unset or empty.
func EnvOrDefault(key string, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// DurationEnvOrDefault parses a duration from an environment variable.
// Returns fallback if the variable is unset, empty, or unparseable.
func DurationEnvOrDefault(key string, fallback time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		return fallback
	}
	return d
}

// ExpandHome expands a leading ~/ to the current user's home directory.
// Returns path unchanged if it does not start with ~/ or home lookup fails.
func ExpandHome(path string) string {
	if len(path) < 2 || path[:2] != "~/" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}
