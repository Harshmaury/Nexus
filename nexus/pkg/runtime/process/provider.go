// @nexus-project: nexus
// @nexus-path: pkg/runtime/process/provider.go
// Package process implements runtime.Provider for local OS processes.
// It manages child processes started via os/exec — suitable for local Go,
// Python, Node, or any binary that runs in the foreground.
//
// PIDs are written to ~/.nexus/pids/<project>.<service>.pid so the provider
// can find and signal processes after a daemon restart.
//
// Config keys (svc.Config JSON): command, args, dir, env, pidFile.
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
	providerName    = "process"
	defaultPIDDir   = "~/.nexus/pids"
	stopGracePeriod = 5 * time.Second
)

// ── CONFIG ────────────────────────────────────────────────────────────────────

// ServiceConfig is the schema for svc.Config JSON for process-provider services.
type ServiceConfig struct {
	Command string   `json:"command"`  // executable path or name on PATH
	Args    []string `json:"args"`     // command arguments
	Dir     string   `json:"dir"`      // working directory, defaults to ~
	Env     []string `json:"env"`      // extra env vars ["KEY=value"]
	PIDFile string   `json:"pidFile"`  // optional override for PID file path
}

// ── PROVIDER ─────────────────────────────────────────────────────────────────

// Provider implements runtime.Provider for local OS processes.
type Provider struct {
	pidBaseDir string
}

// New creates a new process provider.
func New() *Provider {
	return &Provider{pidBaseDir: expandHome(defaultPIDDir)}
}

// Name implements runtime.Provider.
func (p *Provider) Name() string { return providerName }

// ── START ────────────────────────────────────────────────────────────────────

// Start spawns the process in the background and writes its PID to a file.
func (p *Provider) Start(ctx context.Context, svc *state.Service) error {
	cfg, err := parseConfig(svc.Config)
	if err != nil {
		return fmt.Errorf("parse config for service %q: %w", svc.ID, err)
	}
	if cfg.Command == "" {
		return fmt.Errorf("service %q config missing required field: command", svc.ID)
	}

	running, err := p.IsRunning(ctx, svc)
	if err != nil {
		return err
	}
	if running {
		return nil // idempotent
	}

	dir := expandHome(cfg.Dir)
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = home
	}

	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...) //nolint:gosec
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), cfg.Env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start process for service %q: %w", svc.ID, err)
	}

	pidFile := p.pidFilePath(svc, cfg)
	if err := os.MkdirAll(filepath.Dir(pidFile), 0755); err != nil {
		return fmt.Errorf("create pid directory: %w", err)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0644); err != nil {
		return fmt.Errorf("write pid file for service %q: %w", svc.ID, err)
	}

	go func() { _ = cmd.Wait() }() // reap — don't block

	return nil
}

// ── STOP ─────────────────────────────────────────────────────────────────────

// Stop sends SIGTERM, waits up to stopGracePeriod, then SIGKILLs if needed.
func (p *Provider) Stop(_ context.Context, svc *state.Service) error {
	cfg, err := parseConfig(svc.Config)
	if err != nil {
		return fmt.Errorf("parse config for service %q: %w", svc.ID, err)
	}

	pid, err := p.readPID(svc, cfg)
	if err != nil || pid == 0 {
		return nil // already gone
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if isProcessGone(err) {
			return p.removePIDFile(svc, cfg)
		}
		return fmt.Errorf("SIGTERM pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(stopGracePeriod)
	for time.Now().Before(deadline) {
		if !p.pidIsAlive(pid) {
			return p.removePIDFile(svc, cfg)
		}
		time.Sleep(200 * time.Millisecond)
	}

	_ = proc.Kill()
	return p.removePIDFile(svc, cfg)
}

// ── IS RUNNING ───────────────────────────────────────────────────────────────

// IsRunning reads the PID file and checks whether the process is still alive.
func (p *Provider) IsRunning(_ context.Context, svc *state.Service) (bool, error) {
	cfg, err := parseConfig(svc.Config)
	if err != nil {
		return false, fmt.Errorf("parse config for service %q: %w", svc.ID, err)
	}

	pid, err := p.readPID(svc, cfg)
	if err != nil || pid == 0 {
		return false, nil
	}

	return p.pidIsAlive(pid), nil
}

// ── PRIVATE HELPERS ──────────────────────────────────────────────────────────

func (p *Provider) pidFilePath(svc *state.Service, cfg *ServiceConfig) string {
	if cfg.PIDFile != "" {
		return expandHome(cfg.PIDFile)
	}
	return filepath.Join(p.pidBaseDir, svc.Project+"."+svc.ID+".pid")
}

func (p *Provider) readPID(svc *state.Service, cfg *ServiceConfig) (int, error) {
	data, err := os.ReadFile(p.pidFilePath(svc, cfg))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("corrupt pid file: %w", err)
	}
	return pid, nil
}

func (p *Provider) removePIDFile(svc *state.Service, cfg *ServiceConfig) error {
	err := os.Remove(p.pidFilePath(svc, cfg))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (p *Provider) pidIsAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func isProcessGone(err error) bool {
	s := err.Error()
	return strings.Contains(s, "process already finished") ||
		strings.Contains(s, "no such process")
}

func parseConfig(raw string) (*ServiceConfig, error) {
	if raw == "" {
		return &ServiceConfig{}, nil
	}
	var cfg ServiceConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal service config: %w", err)
	}
	return &cfg, nil
}

func expandHome(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
