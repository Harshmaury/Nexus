// @nexus-project: nexus
// @nexus-path: internal/controllers/health.go
// HealthController polls every registered service on a fixed interval,
// records the result in health_logs, updates actual_state in the store,
// and publishes events so the reconciler and recovery controller can react.
// It runs as a separate goroutine from the reconciler — never blocks it.
//
// Phase 7 changes:
//
//   7.1 — Removed local defaultHealthInterval and defaultHealthTimeout constants.
//         Both now come from internal/config, the single source of truth.
//         (Consistent with engine.go, recovery.go, project_controller.go.)
//
//   7.4 — TopicServiceCrashed and TopicRecoveryNeeded are now published via
//         bus.PublishAsync instead of bus.Publish.
//
//         The previous synchronous Publish meant RecoveryController's
//         onRecoveryNeeded handler ran inside HealthController's polling goroutine
//         before Publish returned. A slow or panicking recovery handler would
//         block or crash the entire health poll cycle for all services.
//
//         PublishAsync dispatches the handler in a new goroutine, so the health
//         loop always continues regardless of recovery latency.
package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/Harshmaury/Nexus/internal/config"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/pkg/runtime"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const healthCheckResultBufSize = 50

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
	store     state.Storer
	bus       *eventbus.Bus
	events    *state.EventWriter
	providers runtime.Providers
	interval  time.Duration
	timeout   time.Duration
	results   chan HealthCheckResult
}

// HealthControllerConfig holds all dependencies for the HealthController.
type HealthControllerConfig struct {
	Store     state.Storer
	Bus       *eventbus.Bus
	Providers runtime.Providers
	Interval  time.Duration
	Timeout   time.Duration
}

// NewHealthController creates a HealthController with required dependencies.
func NewHealthController(cfg HealthControllerConfig) *HealthController {
	interval := cfg.Interval
	if interval == 0 {
		interval = config.DefaultHealthInterval // from internal/config, not a local const
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = config.DefaultHealthTimeout // from internal/config, not a local const
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

// Interval returns the configured health poll interval.
func (h *HealthController) Interval() time.Duration {
	return h.interval
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
func (h *HealthController) checkService(ctx context.Context, svc *state.Service) HealthCheckResult {
	start := time.Now()

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

// handleResult logs the health metric and publishes events for state transitions.
// It does NOT write actual_state — that is the reconciler Engine's sole responsibility.
func (h *HealthController) handleResult(svc *state.Service, result HealthCheckResult) {
	traceID := fmt.Sprintf("health-%s-%d", svc.ID, result.CheckedAt.UnixNano())

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

	// HealthController never writes actual_state — the reconciler Engine is the
	// sole owner of that column (Fix 06). Health controller responsibilities:
	//   1. LogHealth  — persist the health metric (done above)
	//   2. Publish events — so Engine and RecoveryController can react
	// The reconciler detects the state change on its next tick via
	// provider.IsRunning() and writes actual_state authoritatively.
	switch result.Status {
	case state.StateRunning:
		if svc.ActualState == state.StateCrashed || svc.ActualState == state.StateRecovering {
			_ = h.events.ServiceHealed(result.ServiceID, traceID)
			// Healed events are informational — synchronous is fine.
			h.bus.Publish(eventbus.TopicServiceHealed, result.ServiceID, eventbus.HealthCheckPayload{
				ServiceID: result.ServiceID,
				Status:    string(result.Status),
				Message:   "service recovered",
			})
		}

	case state.StateCrashed:
		_ = h.events.ServiceCrashed(result.ServiceID, traceID, result.ExitCode, result.Message)

		// PublishAsync: crash events trigger the RecoveryController's onRecoveryNeeded
		// handler, which talks to the store and can be slow. Running it synchronously
		// would block this health poll goroutine for all other services.
		h.bus.PublishAsync(eventbus.TopicServiceCrashed, result.ServiceID, eventbus.HealthCheckPayload{
			ServiceID: result.ServiceID,
			Status:    string(result.Status),
			ExitCode:  result.ExitCode,
			Message:   result.Message,
		})

		// TopicRecoveryNeeded triggers onRecoveryNeeded in RecoveryController.
		// Must be async for the same reason — recovery policy evaluation involves
		// store reads and writes and must not block the health loop.
		h.bus.PublishAsync(eventbus.TopicRecoveryNeeded, result.ServiceID, eventbus.RecoveryPayload{
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

func (h *HealthController) publishResult(result HealthCheckResult) {
	select {
	case h.results <- result:
	default:
		select {
		case <-h.results:
		default:
		}
		h.results <- result
	}
}
