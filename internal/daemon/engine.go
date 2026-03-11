// @nexus-project: nexus
// @nexus-path: internal/daemon/engine.go
// Package daemon contains the Nexus reconciler — the heart of engxd.
// Every tick it compares desired state vs actual state for every service
// and drives the system toward the desired state using the correct provider.
// It never acts on a single service in isolation — it always reconciles all.
package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/pkg/runtime"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	defaultReconcileInterval = 5 * time.Second
	maxFailuresBeforeMaintenance = 3
	failureWindowMinutes         = 60
)

// ── RECONCILE RESULT ─────────────────────────────────────────────────────────

// ReconcileError captures a single service failure during a reconcile cycle.
type ReconcileError struct {
	ServiceID string
	Action    string // "start" | "stop" | "health-check"
	Err       error
}

func (e ReconcileError) Error() string {
	return fmt.Sprintf("[%s] %s failed: %v", e.ServiceID, e.Action, e.Err)
}

// ReconcileResult is the structured outcome of one reconcile cycle.
// The daemon logs it. The CLI can display it. Tests assert on it.
type ReconcileResult struct {
	CycleID    string
	Started    []string         // service IDs that were started this cycle
	Stopped    []string         // service IDs that were stopped this cycle
	Maintained []string         // service IDs moved to maintenance mode
	Skipped    []string         // service IDs already in correct state
	Errors     []ReconcileError // non-fatal errors (other services still reconciled)
	Duration   time.Duration
	TickedAt   time.Time
}

// HasErrors returns true if any service failed during this cycle.
func (r *ReconcileResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// Summary returns a one-line human-readable summary of the cycle.
func (r *ReconcileResult) Summary() string {
	return fmt.Sprintf(
		"cycle=%s started=%d stopped=%d maintained=%d skipped=%d errors=%d duration=%s",
		r.CycleID,
		len(r.Started),
		len(r.Stopped),
		len(r.Maintained),
		len(r.Skipped),
		len(r.Errors),
		r.Duration.Round(time.Millisecond),
	)
}

// ── ENGINE ───────────────────────────────────────────────────────────────────

// Engine is the Nexus reconciler.
// It runs a tight loop, comparing desired vs actual state,
// and calling the correct Provider to fix any mismatch.
type Engine struct {
	store     *state.Store
	bus       *eventbus.Bus
	events    *state.EventWriter
	providers runtime.Providers
	interval  time.Duration
	results   chan ReconcileResult
}

// EngineConfig holds all dependencies for the Engine.
type EngineConfig struct {
	Store     *state.Store
	Bus       *eventbus.Bus
	Providers runtime.Providers
	Interval  time.Duration
}

// NewEngine creates a new reconciler Engine.
func NewEngine(cfg EngineConfig) *Engine {
	interval := cfg.Interval
	if interval == 0 {
		interval = defaultReconcileInterval
	}

	return &Engine{
		store:     cfg.Store,
		bus:       cfg.Bus,
		events:    state.NewEventWriter(cfg.Store, state.SourceDaemon),
		providers: cfg.Providers,
		interval:  interval,
		results:   make(chan ReconcileResult, 10),
	}
}

// ── RUN ──────────────────────────────────────────────────────────────────────

// Run starts the reconcile loop and blocks until the context is cancelled.
// Call this in a goroutine from the daemon entry point.
func (e *Engine) Run(ctx context.Context) error {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	// Run one cycle immediately on start — don't wait for first tick.
	result := e.reconcile(ctx)
	e.publishResult(result)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			result := e.reconcile(ctx)
			e.publishResult(result)
		}
	}
}

// Results returns a read-only channel of reconcile results.
// The daemon can subscribe to log or display them.
func (e *Engine) Results() <-chan ReconcileResult {
	return e.results
}

// Interval returns the configured reconcile interval.
func (e *Engine) Interval() time.Duration {
	return e.interval
}

// ── RECONCILE CYCLE ──────────────────────────────────────────────────────────

// reconcile runs one full reconcile cycle across all services.
// It is the core of the engine — keep this function readable.
func (e *Engine) reconcile(ctx context.Context) ReconcileResult {
	start := time.Now()
	result := ReconcileResult{
		CycleID:  generateCycleID(),
		TickedAt: start,
	}

	services, err := e.store.GetAllServices()
	if err != nil {
		result.Errors = append(result.Errors, ReconcileError{
			ServiceID: "store",
			Action:    "get-all-services",
			Err:       fmt.Errorf("cannot read services: %w", err),
		})
		result.Duration = time.Since(start)
		return result
	}

	for _, svc := range services {
		action := e.reconcileService(ctx, svc, result.CycleID, &result)
		switch action {
		case "started":
			result.Started = append(result.Started, svc.ID)
		case "stopped":
			result.Stopped = append(result.Stopped, svc.ID)
		case "maintenance":
			result.Maintained = append(result.Maintained, svc.ID)
		case "skipped":
			result.Skipped = append(result.Skipped, svc.ID)
		}
	}

	result.Duration = time.Since(start)
	return result
}

// reconcileService drives a single service toward its desired state.
// Returns the action taken: "started" | "stopped" | "maintenance" | "skipped" | "error"
// All errors are appended to result before returning "error" — never silent.
func (e *Engine) reconcileService(ctx context.Context, svc *state.Service, cycleID string, result *ReconcileResult) string {
	traceID := fmt.Sprintf("%s-%s", cycleID, svc.ID)

	// Services in maintenance mode are not touched by the reconciler.
	// A human must inspect and manually clear them.
	// TODO: move maintenance threshold check to recovery controller (06)
	if svc.ActualState == state.StateMaintenance {
		return "skipped"
	}

	// Check current actual state from the provider.
	actualState, err := e.checkActualState(ctx, svc)
	if err != nil {
		_ = e.store.SetActualState(svc.ID, state.StateUnknown)
		result.Errors = append(result.Errors, ReconcileError{
			ServiceID: svc.ID,
			Action:    "check-actual-state",
			Err:       err,
		})
		return "error"
	}

	// Sync actual state to DB if it changed externally.
	if actualState != svc.ActualState {
		if err := e.store.SetActualState(svc.ID, actualState); err != nil {
			result.Errors = append(result.Errors, ReconcileError{
				ServiceID: svc.ID,
				Action:    "sync-actual-state",
				Err:       err,
			})
			return "error"
		}
		svc.ActualState = actualState
	}

	// States match — nothing to do.
	if svc.DesiredState == svc.ActualState {
		return "skipped"
	}

	// ── DESIRED: RUNNING, ACTUAL: NOT RUNNING ────────────────────────────────
	if svc.DesiredState == state.StateRunning && svc.ActualState != state.StateRunning {

		// TODO: move this failure threshold check to recovery controller (06)
		failures, err := e.store.GetRecentFailures(svc.ID, failureWindowMinutes)
		if err == nil && failures >= maxFailuresBeforeMaintenance {
			_ = e.store.SetActualState(svc.ID, state.StateMaintenance)
			_ = e.events.SystemAlert(
				"critical",
				fmt.Sprintf("service %s moved to maintenance after %d failures", svc.ID, failures),
				map[string]string{"service_id": svc.ID, "fail_count": fmt.Sprintf("%d", failures)},
			)
			e.bus.Publish(eventbus.TopicSystemAlert, svc.ID, eventbus.AlertPayload{
				Severity: "critical",
				Message:  fmt.Sprintf("%s exceeded failure threshold — maintenance mode", svc.ID),
			})
			return "maintenance"
		}

		// Start the service.
		if err := e.startService(ctx, svc, traceID); err != nil {
			_ = e.store.IncrementFailCount(svc.ID)
			_ = e.store.SetActualState(svc.ID, state.StateCrashed)
			_ = e.events.ServiceCrashed(svc.ID, traceID, -1, err.Error())
			e.bus.Publish(eventbus.TopicServiceCrashed, svc.ID, eventbus.HealthCheckPayload{
				ServiceID: svc.ID,
				Status:    string(state.StateCrashed),
				Message:   err.Error(),
			})
			result.Errors = append(result.Errors, ReconcileError{
				ServiceID: svc.ID,
				Action:    "start",
				Err:       err,
			})
			return "error"
		}

		_ = e.store.ResetFailCount(svc.ID)
		_ = e.store.SetActualState(svc.ID, state.StateRunning)
		_ = e.events.ServiceStarted(svc.ID, traceID)
		e.bus.Publish(eventbus.TopicServiceStarted, svc.ID, nil)
		return "started"
	}

	// ── DESIRED: STOPPED, ACTUAL: RUNNING ────────────────────────────────────
	if svc.DesiredState == state.StateStopped && svc.ActualState == state.StateRunning {
		if err := e.stopService(ctx, svc, traceID); err != nil {
			_ = e.events.SystemAlert("warn",
				fmt.Sprintf("failed to stop service %s: %v", svc.ID, err),
				map[string]string{"service_id": svc.ID},
			)
			result.Errors = append(result.Errors, ReconcileError{
				ServiceID: svc.ID,
				Action:    "stop",
				Err:       err,
			})
			return "error"
		}

		_ = e.store.SetActualState(svc.ID, state.StateStopped)
		_ = e.events.ServiceStopped(svc.ID, traceID)
		e.bus.Publish(eventbus.TopicServiceStopped, svc.ID, nil)
		return "stopped"
	}

	return "skipped"
}

// ── PROVIDER DISPATCH ────────────────────────────────────────────────────────

// checkActualState asks the provider whether the service is running.
func (e *Engine) checkActualState(ctx context.Context, svc *state.Service) (state.ServiceState, error) {
	provider, err := e.getProvider(svc.Provider)
	if err != nil {
		return state.StateUnknown, err
	}

	running, err := provider.IsRunning(ctx, svc)
	if err != nil {
		return state.StateUnknown, fmt.Errorf("provider %s IsRunning: %w", provider.Name(), err)
	}

	if running {
		return state.StateRunning, nil
	}
	return state.StateStopped, nil
}

// startService calls the correct provider to start a service.
func (e *Engine) startService(ctx context.Context, svc *state.Service, traceID string) error {
	provider, err := e.getProvider(svc.Provider)
	if err != nil {
		return err
	}

	_ = e.store.SetActualState(svc.ID, state.StateRecovering)
	_ = e.events.StateChanged(svc.ID, traceID, string(svc.ActualState), string(state.StateRecovering))

	if err := provider.Start(ctx, svc); err != nil {
		return fmt.Errorf("provider %s Start: %w", provider.Name(), err)
	}
	return nil
}

// stopService calls the correct provider to stop a service.
func (e *Engine) stopService(ctx context.Context, svc *state.Service, traceID string) error {
	provider, err := e.getProvider(svc.Provider)
	if err != nil {
		return err
	}

	if err := provider.Stop(ctx, svc); err != nil {
		return fmt.Errorf("provider %s Stop: %w", provider.Name(), err)
	}
	return nil
}

// getProvider returns the provider for a given type.
// This is the only place provider type is resolved — no switch statements elsewhere.
func (e *Engine) getProvider(providerType state.ProviderType) (runtime.Provider, error) {
	provider, exists := e.providers[providerType]
	if !exists {
		return nil, fmt.Errorf("no provider registered for type %q", providerType)
	}
	return provider, nil
}

// ── PUBLISH ──────────────────────────────────────────────────────────────────

// publishResult sends the result to the results channel (non-blocking).
func (e *Engine) publishResult(result ReconcileResult) {
	select {
	case e.results <- result:
	default:
		// Channel full — drop oldest result, never block the reconciler.
		select {
		case <-e.results:
		default:
		}
		e.results <- result
	}
}

// ── HELPERS ──────────────────────────────────────────────────────────────────

func generateCycleID() string {
	return fmt.Sprintf("cycle-%d", time.Now().UnixNano())
}
