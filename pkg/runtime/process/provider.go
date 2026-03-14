// @nexus-project: nexus
// @nexus-path: pkg/runtime/process/provider.go
// Package process implements runtime.Provider for local OS processes.
// Every process Nexus manages runs as a background child of engxd, with its
// PID persisted to ~/.nexus/pids/<service-id>.pid so IsRunning survives
// daemon restarts during a back-off window.
//
// CONFIG SCHEMA (svc.Config is JSON):
//
//	{
//	  "command": "go",
//	  "args":    ["run", "./cmd/server/"],
//	  "dir":     "~/workspace/projects/apps/nexus",
//	  "env":     ["PORT=8090", "LOG_LEVEL=debug"],
//	  "log":     "~/.nexus/logs/my-service.log"
//	}
//
//	command  required — binary name or absolute path
//	args     optional — passed verbatim to the binary
//	dir      optional — working directory; ~ is expanded; defaults to $HOME
//	env      optional — KEY=VALUE pairs appended to the inherited env
//	log      optional — combined stdout+stderr file; default: ~/.nexus/logs/<id>.log
//
// SHUTDOWN SEQUENCE:
//
//	SIGTERM to process group → wait up to stopGracePeriod (10s) → SIGKILL
//
// IDEMPOTENCY:
//
//	Start: if process is already alive, returns nil immediately.
//	Stop:  if PID file is missing, returns nil immediately.
package process

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	stopGracePeriod = 10 * time.Second
	pidDirName      = ".nexus/pids"
	logDirName      = ".nexus/logs"
)

// ── CONFIG ───────────────────────────────────────────────────────────────────

// processConfig is the schema for svc.Config JSON for process-provider services.
type processConfig struct {
	Command string   `json:"command"` // required
	Args    []string `json:"args"`
	Dir     string   `json:"dir"`
	Env     []string `json:"env"`
	Log     string   `json:"log"`
}

func parseConfig(raw string) (processConfig, error) {
	if raw == "" || raw == "{}" {
		return processConfig{}, fmt.Errorf("process: config is empty — set at minimum: {\"command\":\"<binary>\"}")
	}
	var cfg processConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return processConfig{}, fmt.Errorf("process: invalid config JSON: %w", err)
	}
	if cfg.Command == "" {
		return processConfig{}, fmt.Errorf("process: config missing required field: command")
	}
	return cfg, nil
}

// ── PROVIDER ─────────────────────────────────────────────────────────────────

// Provider implements runtime.Provider for local OS processes.
// Thread-safe — no shared mutable state beyond the filesystem.
type Provider struct {
	pidDir string
	logDir string
}

// New creates a Process Provider and ensures ~/.nexus/pids/ and ~/.nexus/logs/ exist.
func New() (*Provider, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("process: resolve home dir: %w", err)
	}

	pidDir := filepath.Join(home, pidDirName)
	logDir := filepath.Join(home, logDirName)

	for _, dir := range []string{pidDir, logDir} {
		if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
			return nil, fmt.Errorf("process: create dir %s: %w", dir, mkErr)
		}
	}

	return &Provider{pidDir: pidDir, logDir: logDir}, nil
}

// Name returns the provider identifier used in logs and state.
func (p *Provider) Name() string { return "process" }

// ── START ────────────────────────────────────────────────────────────────────

// Start launches the process in the background and writes its PID to disk.
// Idempotent: if the process is already running, returns nil.
func (p *Provider) Start(ctx context.Context, svc *state.Service) error {
	running, err := p.IsRunning(ctx, svc)
	if err != nil {
		return fmt.Errorf("process: pre-start check for %s: %w", svc.ID, err)
	}
	if running {
		return nil
	}

	cfg, err := parseConfig(svc.Config)
	if err != nil {
		return err
	}

	workDir := expandHome(cfg.Dir)
	if workDir == "" {
		workDir, _ = os.UserHomeDir()
	}

	logPath := cfg.Log
	if logPath == "" {
		logPath = filepath.Join(p.logDir, svc.ID+".log")
	} else {
		logPath = expandHome(logPath)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("process: open log %s: %w", logPath, err)
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = workDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), cfg.Env...)
	// Setpgid puts the child in its own process group so Stop() sends
	// SIGTERM/-SIGKILL to the whole group, not just the top-level binary.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("process: exec %s: %w", cfg.Command, err)
	}

	if err := p.writePID(svc.ID, cmd.Process.Pid); err != nil {
		// Process running but untracked — kill to avoid orphan.
		cmd.Process.Kill()
		logFile.Close()
		return fmt.Errorf("process: write PID for %s: %w", svc.ID, err)
	}

	// Reap the child in the background so it never becomes a zombie.
	// Clean up the PID file once it exits naturally.
	go func() {
		defer logFile.Close()
		cmd.Wait()
		p.removePID(svc.ID)
	}()

	return nil
}

// ── STOP ─────────────────────────────────────────────────────────────────────

// Stop sends SIGTERM to the process group, waits stopGracePeriod, then SIGKILLs.
// Idempotent: if the PID file is missing, returns nil.
func (p *Provider) Stop(ctx context.Context, svc *state.Service) error {
	pid, err := p.readPID(svc.ID)
	if err != nil {
		return nil // no PID file — already stopped
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		p.removePID(svc.ID)
		return nil
	}

	// Negative PID sends to the whole process group (set by Setpgid above).
	if termErr := syscall.Kill(-pid, syscall.SIGTERM); termErr != nil {
		if termErr == syscall.ESRCH {
			p.removePID(svc.ID)
			return nil
		}
	}

	// Poll for clean exit up to stopGracePeriod.
	deadline := time.After(stopGracePeriod)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			syscall.Kill(-pid, syscall.SIGKILL)
			p.removePID(svc.ID)
			return nil
		case <-ticker.C:
			if probeErr := proc.Signal(syscall.Signal(0)); probeErr != nil {
				p.removePID(svc.ID)
				return nil // exited cleanly
			}
		}
	}
}

// ── IS RUNNING ────────────────────────────────────────────────────────────────

// IsRunning returns true if the PID file exists and the process responds to
// signal 0 (a zero-signal probe — no side effects on the process).
func (p *Provider) IsRunning(ctx context.Context, svc *state.Service) (bool, error) {
	pid, err := p.readPID(svc.ID)
	if err != nil {
		return false, nil // no PID file → not running
	}

	probeErr := syscall.Kill(pid, 0)
	if probeErr == nil || probeErr == syscall.EPERM {
		return true, nil // nil = alive and owned; EPERM = alive but not owned
	}
	if probeErr == syscall.ESRCH {
		p.removePID(svc.ID) // stale file
		return false, nil
	}
	return false, fmt.Errorf("process: probe pid %d: %w", pid, probeErr)
}

// ── PID FILE ─────────────────────────────────────────────────────────────────

func (p *Provider) pidPath(serviceID string) string {
	return filepath.Join(p.pidDir, serviceID+".pid")
}

func (p *Provider) writePID(serviceID string, pid int) error {
	return os.WriteFile(p.pidPath(serviceID), []byte(strconv.Itoa(pid)), 0644)
}

func (p *Provider) readPID(serviceID string) (int, error) {
	data, err := os.ReadFile(p.pidPath(serviceID))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("corrupt PID file for %s: %w", serviceID, err)
	}
	return pid, nil
}

func (p *Provider) removePID(serviceID string) {
	os.Remove(p.pidPath(serviceID))
}

// ── UTILITIES ────────────────────────────────────────────────────────────────

// expandHome replaces a leading ~/ with the user home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}
