// @nexus-project: nexus
// @nexus-path: internal/controllers/health.go
// HealthController polls every registered service on a fixed interval,
// records the result in health_logs, updates actual_state in the store,
// and publishes events so the reconciler and recovery controller can react.
// It runs as a separate goroutine from the reconciler — never blocks it.
package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	defaultHealthInterval    = 10 * time.Second
	defaultHealthTimeout     = 5 * time.Second
	healthCheckResultBufSize = 50
)

// ── MODELS ───────────────────────────────────────────────────────────────────

// HealthCheckResult is the outcome of a single health check for one service.
type HealthCheckResult struct {
	ServiceID string
	Status    state.ServiceState
	ExitCode  int
	Message   string
	CheckedAt time.Time
	Duration  time.Duration
}

// IsHealthy returns true if the service is confirmed running.
func (r HealthCheckResult) IsHealthy() bool {
	return r.Status == state.StateRunning
}

// ── HEALTH CONTROLLER ────────────────────────────────────────────────────────

// HealthController polls every registered service and records results.
// It is purely observational — it never changes desired state.
// Only the reconciler changes state. HealthController feeds it data.
type HealthController struct {
	store     *state.Store
	bus       *eventbus.Bus
	events    *state.EventWriter
	providers map[state.ProviderType]daemon.Provider
	interval  time.Duration
	timeout   time.Duration
	results   chan HealthCheckResult
}

// HealthControllerConfig holds all dependencies for the HealthController.
type HealthControllerConfig struct {
	Store     *state.Store
	Bus       *eventbus.Bus
	Providers map[state.ProviderType]daemon.Provider
	Interval  time.Duration // defaults to 10s if zero
	Timeout   time.Duration // per-check timeout, defaults to 5s if zero
}

// NewHealthController creates a HealthController with required dependencies.
func NewHealthController(cfg HealthControllerConfig) *HealthController {
	interval := cfg.Interval
	if interval == 0 {
		interval = defaultHealthInterval
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultHealthTimeout
	}

	return &HealthController{
		store:     cfg.Store,
		bus:       cfg.Bus,
		events:    state.NewEventWriter(cfg.Store, state.SourceHealthPlugin),
		providers: cfg.Providers,
		interval:  interval,
		timeout:   timeout,
		results:   make(chan HealthCheckResult, healthCheckResultBufSize),
	}
}

// ── RUN ──────────────────────────────────────────────────────────────────────

// Run starts the health polling loop and blocks until ctx is cancelled.
// Call this in a goroutine from the daemon entry point.
func (h *HealthController) Run(ctx context.Context) error {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	// Poll immediately on start — don't wait for first tick.
	h.pollAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			h.pollAll(ctx)
		}
	}
}

// Results returns a read-only channel of health check results.
func (h *HealthController) Results() <-chan HealthCheckResult {
	return h.results
}

// ── POLL ALL ─────────────────────────────────────────────────────────────────

// pollAll checks every service that has desired_state = running.
// Services that are desired stopped are not polled — no point checking them.
func (h *HealthController) pollAll(ctx context.Context) {
	services, err := h.store.GetRunningServices()
	if err != nil {
		_ = h.events.SystemAlert("warn",
			fmt.Sprintf("health controller: failed to load services: %v", err),
			map[string]string{"source": "health-controller"},
		)
		return
	}

	for _, svc := range services {
		result := h.checkService(ctx, svc)
		h.handleResult(svc, result)
		h.publishResult(result)
	}
}

// ── CHECK ONE SERVICE ────────────────────────────────────────────────────────

// checkService performs a single health check against the provider.
// Always returns a result — never panics or returns nil.
func (h *HealthController) checkService(ctx context.Context, svc *state.Service) HealthCheckResult {
	start := time.Now()

	// Per-check timeout — prevents one slow provider from blocking all checks.
	checkCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	provider, exists := h.providers[svc.Provider]
	if !exists {
		return HealthCheckResult{
			ServiceID: svc.ID,
			Status:    state.StateUnknown,
			ExitCode:  -1,
			Message:   fmt.Sprintf("no provider registered for type %q", svc.Provider),
			CheckedAt: start,
			Duration:  time.Since(start),
		}
	}

	running, err := provider.IsRunning(checkCtx, svc)
	duration := time.Since(start)

	if err != nil {
		return HealthCheckResult{
			ServiceID: svc.ID,
			Status:    state.StateUnknown,
			ExitCode:  -1,
			Message:   fmt.Sprintf("provider check failed: %v", err),
			CheckedAt: start,
			Duration:  duration,
		}
	}

	if running {
		return HealthCheckResult{
			ServiceID: svc.ID,
			Status:    state.StateRunning,
			ExitCode:  0,
			Message:   "ok",
			CheckedAt: start,
			Duration:  duration,
		}
	}

	// Not running — interpret based on desired state.
	// If desired = running, the service should be up → crashed.
	// If desired = stopped, not running is correct → stopped (not a crash).
	notRunningStatus := state.StateCrashed
	notRunningMessage := "provider reports service not running"
	if svc.DesiredState != state.StateRunning {
		notRunningStatus = state.StateStopped
		notRunningMessage = "service is stopped as desired"
	}

	return HealthCheckResult{
		ServiceID: svc.ID,
		Status:    notRunningStatus,
		ExitCode:  1,
		Message:   notRunningMessage,
		CheckedAt: start,
		Duration:  duration,
	}
}

// ── HANDLE RESULT ────────────────────────────────────────────────────────────

// handleResult persists the result and publishes events for state transitions.
func (h *HealthController) handleResult(svc *state.Service, result HealthCheckResult) {
	traceID := fmt.Sprintf("health-%s-%d", svc.ID, result.CheckedAt.UnixNano())

	// Always log to health_logs — this is the audit trail.
	if err := h.store.LogHealth(
		result.ServiceID,
		result.Status,
		result.ExitCode,
		result.Message,
	); err != nil {
		_ = h.events.SystemAlert("warn",
			fmt.Sprintf("failed to log health for %s: %v", result.ServiceID, err),
			map[string]string{"service_id": result.ServiceID},
		)
	}

	// Only update actual_state and emit events when state has changed.
	if result.Status == svc.ActualState {
		return
	}

	// State changed — update the store.
	if err := h.store.SetActualState(result.ServiceID, result.Status); err != nil {
		_ = h.events.SystemAlert("warn",
			fmt.Sprintf("failed to update actual state for %s: %v", result.ServiceID, err),
			map[string]string{"service_id": result.ServiceID},
		)
		return
	}

	// Emit typed events for state transitions.
	switch result.Status {
	case state.StateRunning:
		if svc.ActualState == state.StateCrashed || svc.ActualState == state.StateRecovering {
			// Was crashed/recovering, now running — healed.
			_ = h.events.ServiceHealed(result.ServiceID, traceID)
			h.bus.Publish(eventbus.TopicServiceHealed, result.ServiceID, eventbus.HealthCheckPayload{
				ServiceID: result.ServiceID,
				Status:    string(result.Status),
				Message:   "service recovered",
			})
		}

	case state.StateCrashed:
		_ = h.events.ServiceCrashed(result.ServiceID, traceID, result.ExitCode, result.Message)
		h.bus.Publish(eventbus.TopicServiceCrashed, result.ServiceID, eventbus.HealthCheckPayload{
			ServiceID: result.ServiceID,
			Status:    string(result.Status),
			ExitCode:  result.ExitCode,
			Message:   result.Message,
		})
		// Publish recovery needed — recovery controller (06) listens to this.
		h.bus.Publish(eventbus.TopicRecoveryNeeded, result.ServiceID, eventbus.RecoveryPayload{
			ServiceID:  result.ServiceID,
			FailCount:  svc.FailCount,
			LastFailed: result.CheckedAt,
		})

	case state.StateUnknown:
		_ = h.events.SystemAlert("warn",
			fmt.Sprintf("service %s state is unknown after health check", result.ServiceID),
			map[string]string{
				"service_id": result.ServiceID,
				"message":    result.Message,
			},
		)
	}
}

// ── PUBLISH RESULT ───────────────────────────────────────────────────────────

// publishResult sends the result to the results channel (non-blocking).
func (h *HealthController) publishResult(result HealthCheckResult) {
	select {
	case h.results <- result:
	default:
		// Channel full — drop oldest, never block the health loop.
		select {
		case <-h.results:
		default:
		}
		h.results <- result
	}
}
