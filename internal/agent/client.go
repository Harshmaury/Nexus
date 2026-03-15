// @nexus-project: nexus
// @nexus-path: internal/agent/client.go
// Package agent implements the engxa remote agent.
//
// An Agent connects to a central engxd over HTTP, registers itself,
// then enters a sync loop:
//
//   1. GET  /agents/<id>/desired  — fetch desired service states
//   2. Reconcile locally using the machine's runtime providers
//   3. POST /agents/<id>/actual   — report actual states back
//   4. POST /agents/<id>/heartbeat — keep registration alive (every 10s)
//
// PROTOCOL — pure HTTP/JSON, zero new dependencies.
// TOKEN AUTH — every request carries X-Nexus-Token header.
//
// RESILIENCE:
//   If the central server is unreachable, the agent continues running
//   services according to the last known desired state and retries
//   on every sync interval (30s default).
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/pkg/runtime"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	defaultSyncInterval      = 30 * time.Second
	defaultHeartbeatInterval = 10 * time.Second
	httpTimeout              = 10 * time.Second
)

// ── CONFIG ────────────────────────────────────────────────────────────────────

// Config holds all configuration for a remote agent.
type Config struct {
	AgentID           string        // stable machine ID; defaults to OS hostname
	ServerURL         string        // e.g. http://192.168.1.10:8080
	Token             string        // shared secret matching agents.token on server
	Address           string        // IP:port this agent can be reached on (informational)
	SyncInterval      time.Duration // default 30s
	HeartbeatInterval time.Duration // default 10s
	Providers         runtime.Providers
}

// ── WIRE TYPES ────────────────────────────────────────────────────────────────

// ServiceDesiredState is what the server wants this agent to run.
type ServiceDesiredState struct {
	ServiceID    string `json:"service_id"`
	DesiredState string `json:"desired_state"`
	Provider     string `json:"provider"`
	Config       string `json:"config"`
	Name         string `json:"name"`
	Project      string `json:"project"`
}

// ServiceActualState is what the agent reports back.
type ServiceActualState struct {
	ServiceID   string `json:"service_id"`
	ActualState string `json:"actual_state"`
	ReportedAt  string `json:"reported_at"`
}

// ── AGENT ─────────────────────────────────────────────────────────────────────

// Agent is the remote engxa process.
type Agent struct {
	cfg         Config
	client      *http.Client
	lastDesired []ServiceDesiredState
}

// New creates an Agent. Returns an error if required fields are missing.
func New(cfg Config) (*Agent, error) {
	if cfg.AgentID == "" {
		h, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("agent: resolve hostname: %w", err)
		}
		cfg.AgentID = h
	}
	if cfg.SyncInterval == 0 {
		cfg.SyncInterval = defaultSyncInterval
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = defaultHeartbeatInterval
	}
	if cfg.ServerURL == "" {
		return nil, fmt.Errorf("agent: ServerURL required")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("agent: Token required")
	}
	return &Agent{cfg: cfg, client: &http.Client{Timeout: httpTimeout}}, nil
}

// ── RUN ──────────────────────────────────────────────────────────────────────

// Run registers this agent then enters the sync/heartbeat loop.
// Blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.register(ctx); err != nil {
		fmt.Printf("[engxa] WARNING: initial registration failed: %v\n", err)
	} else {
		fmt.Printf("[engxa] Registered with %s (agent=%s)\n", a.cfg.ServerURL, a.cfg.AgentID)
	}

	syncTick      := time.NewTicker(a.cfg.SyncInterval)
	heartbeatTick := time.NewTicker(a.cfg.HeartbeatInterval)
	defer syncTick.Stop()
	defer heartbeatTick.Stop()

	a.sync(ctx) // immediate first sync

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeatTick.C:
			if err := a.heartbeat(ctx); err != nil {
				fmt.Printf("[engxa] heartbeat: %v\n", err)
			}
		case <-syncTick.C:
			_ = a.register(ctx) // re-register in case server restarted
			a.sync(ctx)
		}
	}
}

// ── REGISTER ─────────────────────────────────────────────────────────────────

func (a *Agent) register(ctx context.Context) error {
	h, _ := os.Hostname()
	body, _ := json.Marshal(map[string]string{
		"id": a.cfg.AgentID, "hostname": h, "address": a.cfg.Address,
	})
	resp, err := a.post(ctx, "/agents/register", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// ── HEARTBEAT ────────────────────────────────────────────────────────────────

func (a *Agent) heartbeat(ctx context.Context) error {
	resp, err := a.post(ctx, fmt.Sprintf("/agents/%s/heartbeat", a.cfg.AgentID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// ── SYNC ─────────────────────────────────────────────────────────────────────

func (a *Agent) sync(ctx context.Context) {
	desired, err := a.fetchDesired(ctx)
	if err != nil {
		fmt.Printf("[engxa] fetch desired: %v — using last known state\n", err)
		desired = a.lastDesired
	} else {
		a.lastDesired = desired
	}

	actuals := a.reconcile(ctx, desired)

	if err := a.reportActual(ctx, actuals); err != nil {
		fmt.Printf("[engxa] report actual: %v\n", err)
	}
}

func (a *Agent) fetchDesired(ctx context.Context) ([]ServiceDesiredState, error) {
	url := fmt.Sprintf("%s/agents/%s/desired", a.cfg.ServerURL, a.cfg.AgentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Nexus-Token", a.cfg.Token)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data []ServiceDesiredState `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return result.Data, nil
}

// ── LOCAL RECONCILE ───────────────────────────────────────────────────────────

func (a *Agent) reconcile(ctx context.Context, desired []ServiceDesiredState) []ServiceActualState {
	actuals := make([]ServiceActualState, 0, len(desired))
	for _, svc := range desired {
		actuals = append(actuals, ServiceActualState{
			ServiceID:   svc.ServiceID,
			ActualState: a.reconcileOne(ctx, svc),
			ReportedAt:  time.Now().UTC().Format(time.RFC3339),
		})
	}
	return actuals
}

func (a *Agent) reconcileOne(ctx context.Context, svc ServiceDesiredState) string {
	provider, exists := a.cfg.Providers[state.ProviderType(svc.Provider)]
	if !exists {
		fmt.Printf("[engxa] no provider %q for service %s — skipped\n", svc.Provider, svc.ServiceID)
		return "unknown"
	}

	stateSvc := &state.Service{
		ID: svc.ServiceID, Name: svc.Name,
		Project: svc.Project, Config: svc.Config,
		Provider: state.ProviderType(svc.Provider),
	}

	running, err := provider.IsRunning(ctx, stateSvc)
	if err != nil {
		fmt.Printf("[engxa] IsRunning %s: %v\n", svc.ServiceID, err)
		return "unknown"
	}

	switch {
	case svc.DesiredState == "running" && !running:
		if err := provider.Start(ctx, stateSvc); err != nil {
			fmt.Printf("[engxa] start %s: %v\n", svc.ServiceID, err)
			return "crashed"
		}
		fmt.Printf("[engxa] ✓ started %s\n", svc.ServiceID)
		return "running"

	case svc.DesiredState == "stopped" && running:
		if err := provider.Stop(ctx, stateSvc); err != nil {
			fmt.Printf("[engxa] stop %s: %v\n", svc.ServiceID, err)
			return "running"
		}
		fmt.Printf("[engxa] ✓ stopped %s\n", svc.ServiceID)
		return "stopped"
	}

	if running {
		return "running"
	}
	return "stopped"
}

func (a *Agent) reportActual(ctx context.Context, actuals []ServiceActualState) error {
	body, _ := json.Marshal(actuals)
	resp, err := a.post(ctx, fmt.Sprintf("/agents/%s/actual", a.cfg.AgentID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

func (a *Agent) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	if body == nil {
		body = []byte("{}")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.cfg.ServerURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build POST %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Nexus-Token", a.cfg.Token)
	return a.client.Do(req)
}
