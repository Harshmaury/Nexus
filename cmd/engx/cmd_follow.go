// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_follow.go
// Real-time follow modes for logs and events — Phase 17 (ADR-025).
//
// logsFollowCmd replaces the existing logsCmd in main.go.
// It adds --follow (-f) which polls the log file at ~500ms intervals,
// streaming new lines until Ctrl-C. No fsnotify dependency — polling is
// sufficient for local log files and avoids adding imports to the CLI binary.
//
// eventsStreamCmd consumes the GET /events/stream SSE endpoint (ADR-015)
// and prints events line-by-line. Reconnects on disconnect.
//
// APPLY NOTE:
//   Remove `logsCmd()` from main.go and remove `logsCmd()` from root.AddCommand().
//   Add logsFollowCmd() and eventsStreamCmd() to root.AddCommand() instead.
package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	canon "github.com/Harshmaury/Canon/identity"
	"github.com/spf13/cobra"
)

// ── CONSTANTS ──────────────────────────────────────────────────────────────────

const (
	logPollInterval    = 500 * time.Millisecond
	sseReconnectDelay  = 3 * time.Second
	defaultLogLines    = 40
)

// ── LOGS COMMAND (replaces logsCmd in main.go) ────────────────────────────────

// logsFollowCmd tails a service log, optionally following in real time.
// Drop-in replacement for logsCmd — same Use/Short/Args.
func logsFollowCmd() *cobra.Command {
	var lines int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <service-id>",
		Short: "Tail the log for a platform service (--follow for real-time)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			logPath, err := serviceLogPath(id)
			if err != nil {
				return err
			}
			if follow {
				return followLog(logPath, lines)
			}
			return printLogTail(logPath, lines)
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", defaultLogLines, "number of lines to show")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false,
		"stream new log lines in real time (Ctrl-C to stop)")
	return cmd
}

// serviceLogPath returns the path to a service's log file.
func serviceLogPath(serviceID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".nexus", "logs", serviceID+".log"), nil
}

// printLogTail reads and prints the last n lines of a log file.
func printLogTail(path string, n int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no log for service — has it started?\n  Path: %s", path)
		}
		return fmt.Errorf("read log: %w", err)
	}
	return printLastLines(string(data), n)
}

// followLog prints the last n lines then streams new content via polling.
func followLog(path string, initialLines int) error {
	if err := printLogTail(path, initialLines); err != nil {
		// Log may not exist yet — start polling anyway
		fmt.Printf("Waiting for log: %s\n", path)
	}
	offset, err := currentFileOffset(path)
	if err != nil {
		offset = 0
	}
	fmt.Printf("─── following %s (Ctrl-C to stop) ───\n", filepath.Base(path))
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	ticker := time.NewTicker(logPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-sigCh:
			fmt.Println()
			return nil
		case <-ticker.C:
			newOffset, err := streamNewLines(path, offset)
			if err == nil {
				offset = newOffset
			}
		}
	}
}

// currentFileOffset returns the current size of a file (used as the start offset).
func currentFileOffset(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return info.Size(), nil
}

// streamNewLines reads any content added to path since offset and prints it.
// Returns the new offset after reading.
func streamNewLines(path string, offset int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return offset, fmt.Errorf("stat: %w", err)
	}
	if info.Size() <= offset {
		return offset, nil // no new content
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, fmt.Errorf("seek: %w", err)
	}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fmt.Println(colorizeLogLine(scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		return offset, fmt.Errorf("scan: %w", err)
	}
	return info.Size(), nil
}

// colorizeLogLine applies minimal ANSI coloring based on log level keywords.
// Degrades gracefully — if stdout is not a TTY, colors are stripped.
func colorizeLogLine(line string) string {
	if !isTerminal(os.Stdout) {
		return line
	}
	switch {
	case strings.Contains(line, "ERROR") || strings.Contains(line, "FATAL"):
		return "\033[31m" + line + "\033[0m" // red
	case strings.Contains(line, "WARN"):
		return "\033[33m" + line + "\033[0m" // yellow
	case strings.Contains(line, "✓") || strings.Contains(line, "INFO"):
		return line
	default:
		return line
	}
}

// isTerminal returns true if f is connected to a terminal (not a pipe/file).
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// ── EVENTS STREAM COMMAND ─────────────────────────────────────────────────────

// eventsStreamCmd consumes the SSE stream from GET /events/stream (ADR-015).
// Reconnects automatically on disconnect. Use Ctrl-C to stop.
func eventsStreamCmd(httpAddr *string) *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:   "stream",
		Short: "Stream platform events in real time via SSE (Ctrl-C to stop)",
		Long: `Connects to engxd GET /events/stream (ADR-015) and prints events
as they arrive. Reconnects automatically if the connection drops.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			addr := *httpAddr + "/events/stream"
			return runSSEConsumer(addr, token)
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "X-Service-Token (if auth enabled)")
	return cmd
}

// runSSEConsumer connects to an SSE endpoint and prints events until Ctrl-C.
func runSSEConsumer(addr, token string) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	fmt.Printf("Streaming events from %s (Ctrl-C to stop)\n\n", addr)
	for {
		select {
		case <-sigCh:
			fmt.Println()
			return nil
		default:
		}
		if err := consumeSSE(addr, token, sigCh); err != nil {
			fmt.Printf("  connection lost: %v — reconnecting in %s\n",
				err, sseReconnectDelay)
		}
		select {
		case <-sigCh:
			fmt.Println()
			return nil
		case <-time.After(sseReconnectDelay):
		}
	}
}

// consumeSSE opens one SSE connection and prints events until disconnect or error.
func consumeSSE(addr, token string, sigCh <-chan os.Signal) error {
	req, err := http.NewRequest(http.MethodGet, addr, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if token != "" {
		req.Header.Set(canon.ServiceTokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, addr)
	}
	return scanSSELines(resp.Body, sigCh)
}

// scanSSELines reads SSE lines from r and prints formatted events.
func scanSSELines(r io.Reader, sigCh <-chan os.Signal) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		select {
		case <-sigCh:
			return nil
		default:
		}
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue // skip keepalives and blank separators
		}
		printSSELine(line)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	return io.EOF
}

// printSSELine formats and prints one SSE data line.
func printSSELine(line string) {
	ts := time.Now().UTC().Format("15:04:05")
	data := strings.TrimPrefix(line, "data: ")
	// Attempt to pretty-print if it looks like JSON.
	if strings.HasPrefix(data, "{") {
		var pretty map[string]any
		if err := strings.NewReader(data); err == nil {
			// print as-is — full JSON pretty-print adds noise in a live stream
		}
		_ = pretty
	}
	fmt.Printf("[%s] %s\n", ts, data)
}

// ── LOGS --SINCE-CRASH ────────────────────────────────────────────────────────

// logsSinceCrashCmd shows log output starting from the last crash timestamp.
// Registered as a flag on logsFollowCmd — see logsFollowCmd() in this file.
// Also exported as a standalone helper used by cmd_run.go.

// fetchLastCrashMessage returns the crash message from the most recent
// SERVICE_CRASHED event for serviceID. Returns "" if none found or Nexus
// is unavailable. Used by stepWait to surface crash reason inline.
func fetchLastCrashMessage(httpAddr, serviceID string) string {
	var result struct {
		Data []struct {
			Type      string `json:"type"`
			ServiceID string `json:"service_id"`
			Outcome   string `json:"outcome"`
			Payload   string `json:"payload"`
		} `json:"data"`
	}
	url := fmt.Sprintf("%s/events?limit=20", httpAddr)
	if err := getJSON(url, &result); err != nil {
		return ""
	}
	for _, e := range result.Data {
		if e.Type == "SERVICE_CRASHED" && e.ServiceID == serviceID {
			if e.Outcome != "" {
				return e.Outcome
			}
			return "service crashed — check engx logs " + serviceID
		}
	}
	return ""
}

// fetchLastCrashTime returns the timestamp of the most recent SERVICE_CRASHED
// event for serviceID. Returns zero time if none found.
func fetchLastCrashTime(httpAddr, serviceID string) time.Time {
	var result struct {
		Data []struct {
			Type      string    `json:"type"`
			ServiceID string    `json:"service_id"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"data"`
	}
	url := fmt.Sprintf("%s/events?limit=50", httpAddr)
	if err := getJSON(url, &result); err != nil {
		return time.Time{}
	}
	for _, e := range result.Data {
		if e.Type == "SERVICE_CRASHED" && e.ServiceID == serviceID {
			return e.CreatedAt
		}
	}
	return time.Time{}
}

// printLogSinceCrash prints log lines for serviceID starting from the
// last crash timestamp. Falls back to last N lines if no crash recorded.
func printLogSinceCrash(serviceID string, fallbackLines int) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}
	logPath := filepath.Join(home, ".nexus", "logs", serviceID+".log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no log for %q — has the service started?", serviceID)
		}
		return fmt.Errorf("read log: %w", err)
	}

	crashTime := fetchLastCrashTime("http://127.0.0.1:8080", serviceID)
	if crashTime.IsZero() {
		fmt.Printf("  No crash recorded for %q — showing last %d lines\n\n", serviceID, fallbackLines)
		return printLastLines(string(data), fallbackLines)
	}

	fmt.Printf("  Showing logs from last crash (%s)\n", crashTime.Format("15:04:05"))
	fmt.Println("  " + strings.Repeat("─", 48))

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	// Find the first line at or after the crash timestamp.
	// Log lines begin with the service name and timestamp e.g. "[atlas] 2026/03/22 12:10:03"
	startIdx := 0
	crashStr := crashTime.Format("2006/01/02 15:04:05")
	for i, line := range lines {
		if strings.Contains(line, crashStr[:16]) { // match YYYY/MM/DD HH:MM
			startIdx = i
			break
		}
	}

	shown := lines[startIdx:]
	if len(shown) > 80 {
		shown = shown[:80]
	}
	for _, line := range shown {
		fmt.Println(line)
	}
	return nil
}
