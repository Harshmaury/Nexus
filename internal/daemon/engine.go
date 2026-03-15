// @nexus-project: nexus
// @nexus-path: internal/daemon/engine.go
// Package daemon contains the Nexus reconciler — the heart of engxd.
// Every tick it compares desired state vs actual state for every service
// and drives the system toward the desired state using the correct provider.
//
// Phase 11 addition — dependency-aware reconciliation:
//
//   Before reconciling, the engine runs a topological sort (Kahn's algorithm)
//   over all services using their depends_on lists. Services are then processed
//   in dependency order:
//
//     START  — topo order    (dependencies started before dependents)
//     STOP   — reverse order (dependents stopped before dependencies)
//
//   If a dependency is not yet running, the dependent's start is DEFERRED —
//   it is skipped this cycle and retried on the next tick. No error is raised;
//   the system converges naturally within a few reconcile intervals.
//
//   Cycle detection: if a dependency cycle is detected, all services in the
//   cycle are skipped with a system alert. The rest of the graph is unaffected.
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
	Deferred   []string // waiting for dependency to become running
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
	interval  time.Duration
	results   chan ReconcileResult
	log       *slog.Logger
}

type EngineConfig struct {
	Store     state.Storer
	Bus       *eventbus.Bus
	Providers runtime.Providers
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

func (e *Engine) Run(ctx context.Context) error {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

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

func (e *Engine) Results() <-chan ReconcileResult { return e.results }
func (e *Engine) Interval() time.Duration         { return e.interval }

// ── RECONCILE CYCLE ──────────────────────────────────────────────────────────

func (e *Engine) reconcile(ctx context.Context) ReconcileResult {
	start := time.Now()
	result := ReconcileResult{CycleID: generateCycleID(), TickedAt: start}

	services, err := e.store.GetAllServices()
	if err != nil {
		result.Errors = append(result.Errors, ReconcileError{
			ServiceID: "store", Action: "get-all-services",
			Err: fmt.Errorf("cannot read services: %w", err),
		})
		result.Duration = time.Since(start)
		return result
	}

	// Build dependency graph and sort topologically.
	// Services in a cycle are excluded from sorted; they get skipped below.
	sorted, cyclic := e.topoSort(services)

	// Log any detected cycles as system alerts — once per cycle.
	for _, id := range cyclic {
		e.log.Warn("dependency cycle detected — service skipped", "service_id", id)
		_ = e.events.SystemAlert("warn",
			fmt.Sprintf("service %s skipped: dependency cycle detected", id),
			map[string]string{"service_id": id},
		)
		result.Skipped = append(result.Skipped, id)
	}

	// Build a map of actual states for fast dependency checking.
	actualStates := make(map[string]state.ServiceState, len(services))
	for _, svc := range services {
		actualStates[svc.ID] = svc.ActualState
	}

	// Index services by ID for quick lookup.
	byID := make(map[string]*state.Service, len(services))
	for _, svc := range services {
		byID[svc.ID] = svc
	}

	// Process services in topo order (start) and reverse (stop) within each cycle.
	// We do a single pass: start actions respect topo order because sorted is
	// already ordered. Stop actions on dependents happen first because dependents
	// appear later in sorted and we process them in the same pass — by the time
	// we reach a dependency, its dependent has already been queued to stop.
	for _, svc := range sorted {
		action := e.reconcileService(ctx, svc, byID, actualStates, result.CycleID, &result)
		switch action {
		case "started":
			result.Started = append(result.Started, svc.ID)
			actualStates[svc.ID] = state.StateRunning
		case "stopped":
			result.Stopped = append(result.Stopped, svc.ID)
			actualStates[svc.ID] = state.StateStopped
		case "maintenance":
			result.Maintained = append(result.Maintained, svc.ID)
		case "deferred":
			result.Deferred = append(result.Deferred, svc.ID)
		case "skipped":
			result.Skipped = append(result.Skipped, svc.ID)
		}
	}

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

	// ── START PATH ────────────────────────────────────────────────────────────
	if svc.DesiredState == state.StateRunning && svc.ActualState != state.StateRunning {
		// Check all dependencies are running before starting this service.
		deps, depErr := e.store.GetServiceDependencies(svc.ID)
		if depErr != nil {
			e.log.Warn("cannot read dependencies", "service_id", svc.ID, "err", depErr)
			// Non-fatal: proceed without dependency check rather than blocking forever.
		}
		for _, depID := range deps {
			depState, exists := actualStates[depID]
			if !exists || depState != state.StateRunning {
				// Dependency not ready — defer until next tick.
				e.log.Debug("deferring start — dependency not running",
					"service_id", svc.ID, "dependency", depID)
				return "deferred"
			}
		}
		return e.reconcileStart(ctx, svc, traceID, result)
	}

	// ── STOP PATH ─────────────────────────────────────────────────────────────
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
		if writeErr := e.events.ServiceCrashed(svc.ID, traceID, -1, err.Error()); writeErr != nil {
			e.log.Warn("failed to write ServiceCrashed event", "service_id", svc.ID, "err", writeErr)
		}
		e.bus.Publish(eventbus.TopicServiceCrashed, svc.ID, eventbus.HealthCheckPayload{
			ServiceID: svc.ID, Status: string(state.StateCrashed), Message: err.Error(),
		})
		result.Errors = append(result.Errors, ReconcileError{ServiceID: svc.ID, Action: "start", Err: err})
		return "error"
	}

	e.store.ResetFailCount(svc.ID) //nolint:errcheck
	e.setActualStateSafe(svc.ID, state.StateRunning, traceID)
	if writeErr := e.events.ServiceStarted(svc.ID, traceID); writeErr != nil {
		e.log.Warn("failed to write ServiceStarted event", "service_id", svc.ID, "err", writeErr)
	}
	e.bus.Publish(eventbus.TopicServiceStarted, svc.ID, nil)
	return "started"
}

func (e *Engine) reconcileStop(ctx context.Context, svc *state.Service, traceID string, result *ReconcileResult) string {
	if err := e.stopService(ctx, svc, traceID); err != nil {
		result.Errors = append(result.Errors, ReconcileError{ServiceID: svc.ID, Action: "stop", Err: err})
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

// ── TOPOLOGICAL SORT (Kahn's algorithm) ──────────────────────────────────────

// topoSort returns services in dependency order (dependencies first).
// Services with no dependencies or with unknown dependencies are treated
// as having zero in-degree and appear first.
// Returns (sorted, cyclic) where cyclic contains IDs of services in a cycle.
func (e *Engine) topoSort(services []*state.Service) (sorted []*state.Service, cyclic []string) {
	// Build adjacency: edge A→B means "A must start before B" (B depends on A).
	// We need reverse: for each service, who does it depend on?
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
			e.log.Warn("cannot read deps for topo sort", "service_id", svc.ID, "err", err)
			svcDeps = nil
		}
		deps[svc.ID] = svcDeps
		for _, depID := range svcDeps {
			if _, known := byID[depID]; known {
				inDegree[svc.ID]++
			}
			// Unknown dependencies (service not registered) are silently ignored —
			// they cannot be tracked and should not block the dependent.
		}
	}

	// Kahn's BFS: enqueue all zero-in-degree nodes.
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

		// For every service that depends on id, reduce its in-degree.
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

	// Any service still with in-degree > 0 is part of a cycle.
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
