// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_shared.go
// cmd_shared.go contains helpers shared across all cmd files.
// No command definitions live here — only pure utilities.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	canon "github.com/Harshmaury/Canon/identity"
	"github.com/Harshmaury/Nexus/internal/daemon"
)

// ── SOCKET CLIENT ─────────────────────────────────────────────────────────────

// sendCommand dials the engxd Unix socket and sends a command.
// Retries up to 3 times with backoff so callers survive a brief daemon startup
// race — e.g. `engxd & sleep 1 && engx platform start` no longer needs the sleep.
func sendCommand(socketPath string, cmd daemon.Command, params any) (*daemon.Response, error) {
	backoff := []time.Duration{0, 300 * time.Millisecond, 700 * time.Millisecond}
	var lastErr error
	for _, wait := range backoff {
		if wait > 0 {
			time.Sleep(wait)
		}
		resp, err := trySendCommand(socketPath, cmd, params)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("daemon not running — start with: engxd (%w)", lastErr)
}

// trySendCommand makes one socket attempt. Returns the daemon error string
// as a Go error if ok=false, so the caller can distinguish transport errors
// from command errors.
func trySendCommand(socketPath string, cmd daemon.Command, params any) (*daemon.Response, error) {
	conn, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		return nil, err // transport error — eligible for retry
	}
	defer conn.Close()

	var rawParams json.RawMessage
	if params != nil {
		rawParams, err = json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("encode params: %w", err)
		}
	}

	req := daemon.Request{
		ID:      fmt.Sprintf("cli-%d", time.Now().UnixNano()),
		Command: cmd,
		Params:  rawParams,
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	var resp daemon.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if !resp.OK {
		// Command error — not a transport error, do not retry.
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return &resp, nil
}

// ── HTTP HELPERS ──────────────────────────────────────────────────────────────

// getJSON performs a GET request and JSON-decodes the response body into out.
func getJSON(url string, out any) error {
	return getJSONWithToken(url, "", out)
}

// getJSONWithToken performs a GET request with an optional X-Service-Token header.
func getJSONWithToken(url, token string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set(canon.ServiceTokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ── FILE HELPERS ──────────────────────────────────────────────────────────────

// fileExists returns true if path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// hasGoCmd returns true if any cmd/<n>/main.go exists under dir.
func hasGoCmd(dir string) bool {
	cmdDir := filepath.Join(dir, "cmd")
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		return fileExists(filepath.Join(dir, "main.go"))
	}
	for _, e := range entries {
		if e.IsDir() && fileExists(filepath.Join(cmdDir, e.Name(), "main.go")) {
			return true
		}
	}
	return false
}

// ── FORMAT HELPERS ────────────────────────────────────────────────────────────

// formatUptime converts seconds to a human-readable duration string.
func formatUptime(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%.0fs", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%.0fm", seconds/60)
	}
	h := int(seconds) / 3600
	m := (int(seconds) % 3600) / 60
	return fmt.Sprintf("%dh %dm", h, m)
}

// truncate shortens s to at most n runes, appending … if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// projectServiceStates returns (running, total) for a project's services.
// Used by run, ci, and automation commands.
func projectServiceStates(httpAddr, projectID string) (int, int, error) {
	var result struct {
		Data []struct {
			ActualState  string `json:"actual_state"`
			DesiredState string `json:"desired_state"`
		} `json:"data"`
	}
	url := fmt.Sprintf("%s/services?project=%s", httpAddr, projectID)
	if err := getJSON(url, &result); err != nil {
		return 0, 0, err
	}
	total, running := 0, 0
	for _, s := range result.Data {
		if s.DesiredState == "stopped" {
			continue
		}
		total++
		if s.ActualState == "running" {
			running++
		}
	}
	return running, total, nil
}
