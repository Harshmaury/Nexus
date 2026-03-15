// @nexus-project: nexus
// @nexus-path: internal/daemon/engine.go
// NX-H-01: publishResult is now context-aware.
//   The old implementation had an unconditional blocking send as its
//   last fallback: if the channel was full AND the drain failed (consumer
//   gone), the reconciler goroutine blocked forever. Under shutdown the
//   logResults goroutine exits first, leaving the reconciler stuck.
//   Fix: publishResult accepts ctx and selects on ctx.Done() alongside
//   the channel send — if context is cancelled the result is dropped
//   silently, which is correct behaviour during shutdown.
//
// Package daemon contains the Nexus reconciler — the heart of engxd.
//
// Phase 12 addition — metrics wired into reconcile:
//   Each cycle increments ReconcileCycle().
//   Per-service actions increment the matching counter.
//   After every cycle the running/maintenance gauges are updated
//   from the result so GET /metrics always reflects current reality.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Harshmaury/Nexus/internal/config"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/internal/telemetry"
	"github.com/Harshmaury/Nexus/pkg/runtime"
)

const defaultReconcileInterval = config.DefaultReconcileInterval

// ── RECONCILE RESULT ─────────────────────────────────────────────────────────

type ReconcileError struct {
	ServiceID string
	Action    string
	Err       error
}

func (e ReconcileError) Error() string {
	return fmt.Sprintf("[%s] %s failed: %v", e.ServiceID, e.Action, e.Err)
}

type ReconcileResult struct {
	CycleID    string
	Started    []string
	Stopped    []string
	Maintained []string
	Skipped    []string
	Deferred   []string
	Errors     []ReconcileError
	Duration   time.Duration
	TickedAt   time.Time
}

func (r *ReconcileResult) HasErrors() bool { return len(r.Errors) > 0 }

func (r *ReconcileResult) Summary() string {
	return fmt.Sprintf(
		"cycle=%s started=%d stopped=%d maintained=%d skipped=%d deferred=%d errors=%d duration=%s",
		r.CycleID, len(r.Started), len(r.Stopped),
		len(r.Maintained), len(r.Skipped), len(r.Deferred),
		len(r.Errors), r.Duration.Round(time.Millisecond),
	)
}

// ── ENGINE ───────────────────────────────────────────────────────────────────

type Engine struct {
	store     state.Storer
	bus       *eventbus.Bus
	events    *state.EventWriter
	providers runtime.Providers
	metrics   *telemetry.Metrics
	interval  time.Duration
	results   chan ReconcileResult
	log       *slog.Logger
}

type EngineConfig struct {
	Store     state.Storer
	Bus       *eventbus.Bus
	Providers runtime.Providers
	Metrics   *telemetry.Metrics // optional — NopMetrics used if nil
	Interval  time.Duration
	Logger    *slog.Logger
}

func NewEngine(cfg EngineConfig) *Engine {
	interval := cfg.Interval
	if interval == 0 {
		interval = defaultReconcileInterval
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = telemetry.New() // always have a valid metrics instance
	}
	return &Engine{
		store:     cfg.Store,
		bus:       cfg.Bus,
		events:    state.NewEventWriter(cfg.Store, state.SourceDaemon),
		providers: cfg.Providers,
		metrics:   metrics,
		interval:  interval,
		results:   make(chan ReconcileResult, 10),
		log:       logger.With("component", "reconciler"),
	}
}

func (e *Engine) Run(ctx context.Context) error {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	e.publishResult(ctx, e.reconcile(ctx))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			e.publishResult(ctx, e.reconcile(ctx))
		}
	}
}

func (e *Engine) Results() <-chan ReconcileResult { return e.results }
func (e *Engine) Interval() time.Duration         { return e.interval }

// ── RECONCILE CYCLE ──────────────────────────────────────────────────────────

func (e *Engine) reconcile(ctx context.Context) ReconcileResult {
	start := time.Now()
	result := ReconcileResult{CycleID: generateCycleID(), TickedAt: start}

	e.metrics.ReconcileCycle()

	services, err := e.store.GetAllServices()
	if err != nil {
		result.Errors = append(result.Errors, ReconcileError{
			ServiceID: "store", Action: "get-all-services",
			Err: fmt.Errorf("cannot read services: %w", err),
		})
		e.metrics.ReconcileError()
		result.Duration = time.Since(start)
		return result
	}

	sorted, cyclic := e.topoSort(services)

	for _, id := range cyclic {
		e.log.Warn("dependency cycle — service skipped", "service_id", id)
		_ = e.events.SystemAlert("warn",
			fmt.Sprintf("service %s skipped: dependency cycle", id),
			map[string]string{"service_id": id},
		)
		result.Skipped = append(result.Skipped, id)
	}

	actualStates := make(map[string]state.ServiceState, len(services))
	for _, svc := range services {
		actualStates[svc.ID] = svc.ActualState
	}

	byID := make(map[string]*state.Service, len(services))
	for _, svc := range services {
		byID[svc.ID] = svc
	}

	for _, svc := range sorted {
		action := e.reconcileService(ctx, svc, byID, actualStates, result.CycleID, &result)
		switch action {
		case "started":
			result.Started = append(result.Started, svc.ID)
			actualStates[svc.ID] = state.StateRunning
			e.metrics.ServiceStarted()
		case "stopped":
			result.Stopped = append(result.Stopped, svc.ID)
			actualStates[svc.ID] = state.StateStopped
			e.metrics.ServiceStopped()
		case "maintenance":
			result.Maintained = append(result.Maintained, svc.ID)
		case "deferred":
			result.Deferred = append(result.Deferred, svc.ID)
			e.metrics.ServiceDeferred()
		case "skipped":
			result.Skipped = append(result.Skipped, svc.ID)
		case "error":
			e.metrics.ReconcileError()
		}
	}

	// Update gauges from final actualStates.
	var running, maintenance int64
	for _, s := range actualStates {
		switch s {
		case state.StateRunning:
			running++
		case state.StateMaintenance:
			maintenance++
		}
	}
	e.metrics.SetRunning(running)
	e.metrics.SetMaintenance(maintenance)

	result.Duration = time.Since(start)
	return result
}

// ── RECONCILE ONE SERVICE ────────────────────────────────────────────────────

func (e *Engine) reconcileService(
	ctx context.Context,
	svc *state.Service,
	byID map[string]*state.Service,
	actualStates map[string]state.ServiceState,
	cycleID string,
	result *ReconcileResult,
) string {
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
		deps, depErr := e.store.GetServiceDependencies(svc.ID)
		if depErr != nil {
			e.log.Warn("cannot read dependencies", "service_id", svc.ID, "err", depErr)
		}
		for _, depID := range deps {
			if depState, exists := actualStates[depID]; !exists || depState != state.StateRunning {
				e.log.Debug("deferring — dependency not running",
					"service_id", svc.ID, "dependency", depID)
				return "deferred"
			}
		}
		return e.reconcileStart(ctx, svc, traceID, result)
	}

	if svc.DesiredState == state.StateStopped && svc.ActualState == state.StateRunning {
		return e.reconcileStop(ctx, svc, traceID, result)
	}

	return "skipped"
}

// ── START / STOP ─────────────────────────────────────────────────────────────

func (e *Engine) reconcileStart(ctx context.Context, svc *state.Service, traceID string, result *ReconcileResult) string {
	if err := e.startService(ctx, svc, traceID); err != nil {
		e.store.IncrementFailCount(svc.ID) //nolint:errcheck
		e.setActualStateSafe(svc.ID, state.StateCrashed, traceID)
		_ = e.events.ServiceCrashed(svc.ID, traceID, -1, err.Error())
		e.bus.Publish(eventbus.TopicServiceCrashed, svc.ID, eventbus.HealthCheckPayload{
			ServiceID: svc.ID, Status: string(state.StateCrashed), Message: err.Error(),
		})
		result.Errors = append(result.Errors, ReconcileError{ServiceID: svc.ID, Action: "start", Err: err})
		e.metrics.ServiceCrashed()
		return "error"
	}
	e.store.ResetFailCount(svc.ID) //nolint:errcheck
	e.setActualStateSafe(svc.ID, state.StateRunning, traceID)
	_ = e.events.ServiceStarted(svc.ID, traceID)
	e.bus.Publish(eventbus.TopicServiceStarted, svc.ID, nil)
	return "started"
}

func (e *Engine) reconcileStop(ctx context.Context, svc *state.Service, traceID string, result *ReconcileResult) string {
	if err := e.stopService(ctx, svc, traceID); err != nil {
		result.Errors = append(result.Errors, ReconcileError{ServiceID: svc.ID, Action: "stop", Err: err})
		return "error"
	}
	e.setActualStateSafe(svc.ID, state.StateStopped, traceID)
	_ = e.events.ServiceStopped(svc.ID, traceID)
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
	return provider.Start(ctx, svc)
}

func (e *Engine) stopService(ctx context.Context, svc *state.Service, traceID string) error {
	provider, err := e.getProvider(svc.Provider)
	if err != nil {
		return err
	}
	return provider.Stop(ctx, svc)
}

func (e *Engine) getProvider(providerType state.ProviderType) (runtime.Provider, error) {
	provider, exists := e.providers[providerType]
	if !exists {
		return nil, fmt.Errorf("no provider registered for type %q", providerType)
	}
	return provider, nil
}

// ── TOPOLOGICAL SORT (Kahn's algorithm) ──────────────────────────────────────

func (e *Engine) topoSort(services []*state.Service) (sorted []*state.Service, cyclic []string) {
	deps := make(map[string][]string, len(services))
	inDegree := make(map[string]int, len(services))
	byID := make(map[string]*state.Service, len(services))

	for _, svc := range services {
		byID[svc.ID] = svc
		inDegree[svc.ID] = 0
	}

	for _, svc := range services {
		svcDeps, err := e.store.GetServiceDependencies(svc.ID)
		if err != nil {
			e.log.Warn("cannot read deps", "service_id", svc.ID, "err", err)
			svcDeps = nil
		}
		deps[svc.ID] = svcDeps
		for _, depID := range svcDeps {
			if _, known := byID[depID]; known {
				inDegree[svc.ID]++
			}
		}
	}

	queue := make([]string, 0, len(services))
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	sorted = make([]*state.Service, 0, len(services))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if svc, ok := byID[id]; ok {
			sorted = append(sorted, svc)
		}
		for _, svc := range services {
			for _, depID := range deps[svc.ID] {
				if depID == id {
					inDegree[svc.ID]--
					if inDegree[svc.ID] == 0 {
						queue = append(queue, svc.ID)
					}
				}
			}
		}
	}

	for id, deg := range inDegree {
		if deg > 0 {
			cyclic = append(cyclic, id)
		}
	}
	return sorted, cyclic
}

// ── SAFE STATE WRITER ────────────────────────────────────────────────────────

func (e *Engine) setActualStateSafe(serviceID string, s state.ServiceState, traceID string) {
	if err := e.store.SetActualState(serviceID, s); err != nil {
		e.log.Error("failed to set actual state",
			"service_id", serviceID, "state", s, "trace_id", traceID, "err", err)
	}
}

// publishResult sends result to the results channel without blocking forever.
//
// Strategy (NX-H-01):
//  1. Non-blocking send — succeeds immediately if there is buffer space.
//  2. If full: drain one stale result to make room, then attempt a
//     context-aware send. If ctx is already cancelled (shutdown in progress),
//     the result is dropped — callers have stopped listening and the data
//     is no longer useful.
func (e *Engine) publishResult(ctx context.Context, result ReconcileResult) {
	select {
	case e.results <- result:
		return // fast path — buffer had space
	default:
	}
	// Buffer full — drain one stale entry to make room.
	select {
	case <-e.results:
	default:
	}
	// Context-aware send: drop silently on shutdown rather than blocking.
	select {
	case e.results <- result:
	case <-ctx.Done():
		// Shutdown in progress — consumer has exited, drop the result.
	}
}

func generateCycleID() string {
	return fmt.Sprintf("cycle-%d", time.Now().UnixNano())
}
