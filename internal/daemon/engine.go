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
	"log/slog"
	"time"

	"github.com/Harshmaury/Nexus/internal/config"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/pkg/runtime"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const defaultReconcileInterval = config.DefaultReconcileInterval

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
func (r *ReconcileResult) HasErrors() bool { return len(r.Errors) > 0 }

// Summary returns a one-line human-readable summary of the cycle.
func (r *ReconcileResult) Summary() string {
	return fmt.Sprintf(
		"cycle=%s started=%d stopped=%d maintained=%d skipped=%d errors=%d duration=%s",
		r.CycleID, len(r.Started), len(r.Stopped),
		len(r.Maintained), len(r.Skipped), len(r.Errors),
		r.Duration.Round(time.Millisecond),
	)
}

// ── ENGINE ───────────────────────────────────────────────────────────────────

// Engine is the Nexus reconciler.
// It runs a tight loop, comparing desired vs actual state,
// and calling the correct Provider to fix any mismatch.
type Engine struct {
	store     state.Storer
	bus       *eventbus.Bus
	events    *state.EventWriter
	providers runtime.Providers
	interval  time.Duration
	results   chan ReconcileResult
	log       *slog.Logger
}

// EngineConfig holds all dependencies for the Engine.
type EngineConfig struct {
	Store     state.Storer
	Bus       *eventbus.Bus
	Providers runtime.Providers
	Interval  time.Duration
	Logger    *slog.Logger
}

// NewEngine creates a new reconciler Engine.
func NewEngine(cfg EngineConfig) *Engine {
	interval := cfg.Interval
	if interval == 0 {
		interval = defaultReconcileInterval
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		store:     cfg.Store,
		bus:       cfg.Bus,
		events:    state.NewEventWriter(cfg.Store, state.SourceDaemon),
		providers: cfg.Providers,
		interval:  interval,
		results:   make(chan ReconcileResult, 10),
		log:       logger.With("component", "reconciler"),
	}
}

// ── RUN ──────────────────────────────────────────────────────────────────────

// Run starts the reconcile loop and blocks until the context is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	// Run one cycle immediately on start — don't wait for first tick.
	e.publishResult(e.reconcile(ctx))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			e.publishResult(e.reconcile(ctx))
		}
	}
}

// Results returns a read-only channel of reconcile results.
func (e *Engine) Results() <-chan ReconcileResult { return e.results }

// Interval returns the configured reconcile interval.
func (e *Engine) Interval() time.Duration { return e.interval }

// ── RECONCILE CYCLE ──────────────────────────────────────────────────────────

func (e *Engine) reconcile(ctx context.Context) ReconcileResult {
	start := time.Now()
	result := ReconcileResult{CycleID: generateCycleID(), TickedAt: start}

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
// Returns: "started" | "stopped" | "maintenance" | "skipped" | "error"
func (e *Engine) reconcileService(ctx context.Context, svc *state.Service, cycleID string, result *ReconcileResult) string {
	traceID := fmt.Sprintf("%s-%s", cycleID, svc.ID)

	if svc.ActualState == state.StateMaintenance {
		return "skipped"
	}

	actualState, err := e.checkActualState(ctx, svc)
	if err != nil {
		e.setActualStateSafe(svc.ID, state.StateUnknown, traceID)
		result.Errors = append(result.Errors, ReconcileError{
			ServiceID: svc.ID, Action: "check-actual-state", Err: err,
		})
		return "error"
	}

	if actualState != svc.ActualState {
		if err := e.store.SetActualState(svc.ID, actualState); err != nil {
			result.Errors = append(result.Errors, ReconcileError{
				ServiceID: svc.ID, Action: "sync-actual-state", Err: err,
			})
			return "error"
		}
		svc.ActualState = actualState
	}

	if svc.DesiredState == svc.ActualState {
		return "skipped"
	}

	if svc.DesiredState == state.StateRunning && svc.ActualState != state.StateRunning {
		return e.reconcileStart(ctx, svc, traceID, result)
	}

	if svc.DesiredState == state.StateStopped && svc.ActualState == state.StateRunning {
		return e.reconcileStop(ctx, svc, traceID, result)
	}

	return "skipped"
}

// ── RECONCILE START / STOP ───────────────────────────────────────────────────

func (e *Engine) reconcileStart(ctx context.Context, svc *state.Service, traceID string, result *ReconcileResult) string {
	if err := e.startService(ctx, svc, traceID); err != nil {
		e.store.IncrementFailCount(svc.ID) //nolint:errcheck — best-effort counter
		e.setActualStateSafe(svc.ID, state.StateCrashed, traceID)
		if writeErr := e.events.ServiceCrashed(svc.ID, traceID, -1, err.Error()); writeErr != nil {
			e.log.Warn("failed to write ServiceCrashed event", "service_id", svc.ID, "err", writeErr)
		}
		e.bus.Publish(eventbus.TopicServiceCrashed, svc.ID, eventbus.HealthCheckPayload{
			ServiceID: svc.ID, Status: string(state.StateCrashed), Message: err.Error(),
		})
		result.Errors = append(result.Errors, ReconcileError{
			ServiceID: svc.ID, Action: "start", Err: err,
		})
		return "error"
	}

	e.store.ResetFailCount(svc.ID) //nolint:errcheck — best-effort counter reset
	e.setActualStateSafe(svc.ID, state.StateRunning, traceID)
	if writeErr := e.events.ServiceStarted(svc.ID, traceID); writeErr != nil {
		e.log.Warn("failed to write ServiceStarted event", "service_id", svc.ID, "err", writeErr)
	}
	e.bus.Publish(eventbus.TopicServiceStarted, svc.ID, nil)
	return "started"
}

func (e *Engine) reconcileStop(ctx context.Context, svc *state.Service, traceID string, result *ReconcileResult) string {
	if err := e.stopService(ctx, svc, traceID); err != nil {
		result.Errors = append(result.Errors, ReconcileError{
			ServiceID: svc.ID, Action: "stop", Err: err,
		})
		return "error"
	}
	e.setActualStateSafe(svc.ID, state.StateStopped, traceID)
	if writeErr := e.events.ServiceStopped(svc.ID, traceID); writeErr != nil {
		e.log.Warn("failed to write ServiceStopped event", "service_id", svc.ID, "err", writeErr)
	}
	e.bus.Publish(eventbus.TopicServiceStopped, svc.ID, nil)
	return "stopped"
}

// ── PROVIDER DISPATCH ────────────────────────────────────────────────────────

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

func (e *Engine) startService(ctx context.Context, svc *state.Service, traceID string) error {
	provider, err := e.getProvider(svc.Provider)
	if err != nil {
		return err
	}
	e.setActualStateSafe(svc.ID, state.StateRecovering, traceID)
	if err := provider.Start(ctx, svc); err != nil {
		return fmt.Errorf("provider %s Start: %w", provider.Name(), err)
	}
	return nil
}

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

func (e *Engine) getProvider(providerType state.ProviderType) (runtime.Provider, error) {
	provider, exists := e.providers[providerType]
	if !exists {
		return nil, fmt.Errorf("no provider registered for type %q", providerType)
	}
	return provider, nil
}

// ── SAFE STATE WRITER ────────────────────────────────────────────────────────

// setActualStateSafe writes state and logs on failure instead of silently discarding.
func (e *Engine) setActualStateSafe(serviceID string, s state.ServiceState, traceID string) {
	if err := e.store.SetActualState(serviceID, s); err != nil {
		e.log.Error("failed to set actual state",
			"service_id", serviceID,
			"state", s,
			"trace_id", traceID,
			"err", err,
		)
	}
}

// ── PUBLISH ──────────────────────────────────────────────────────────────────

func (e *Engine) publishResult(result ReconcileResult) {
	select {
	case e.results <- result:
	default:
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
