// @nexus-project: nexus
// @nexus-path: internal/controllers/recovery.go
// RecoveryController listens for TopicRecoveryNeeded events from the health controller
// and decides whether to attempt a restart or escalate to maintenance mode.
// This is where all restart policy lives — the reconciler and health controller
// are policy-free. Only RecoveryController makes recovery decisions.
//
// Phase 7.3 change — back-off state is now persisted to the store:
//
//   Previously: RecoveryController held a pending map[string]time.Time in memory.
//   Problem:    If the daemon was killed during a back-off window, the map was lost.
//               On restart the service would either restart immediately (skipping
//               the delay) or stall indefinitely.
//
//   Now:        execute(BackOff) calls store.SetRestartAfter(id, restartAt).
//               processPending() calls store.GetServicesReadyToRestart() and
//               then store.ClearRestartAfter(id) after re-queueing.
//               Daemon restarts during a back-off window honour the delay correctly.
//
//   Removed:    pending map[string]time.Time
//   Removed:    sync.Mutex (no longer needed — store is the source of truth)
package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/Harshmaury/Nexus/internal/config"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ── RECOVERY DECISION ────────────────────────────────────────────────────────

// RecoveryAction is the decision the controller makes for a crashed service.
type RecoveryAction string

const (
	RecoveryActionRestart     RecoveryAction = "restart"
	RecoveryActionBackOff     RecoveryAction = "back-off"
	RecoveryActionMaintenance RecoveryAction = "maintenance"
	RecoveryActionSkip        RecoveryAction = "skip"
)

// RecoveryDecision is the full structured decision for one service.
type RecoveryDecision struct {
	ServiceID    string
	Action       RecoveryAction
	BackOffDelay time.Duration // non-zero when action = back-off
	Reason       string
	DecidedAt    time.Time
}

// ── RECOVERY CONTROLLER ──────────────────────────────────────────────────────

// RecoveryController subscribes to crash events and enforces restart policy.
// Policy is sourced entirely from internal/config — no magic numbers here.
// Back-off state is persisted in the store (restart_after column) so that
// daemon restarts during a back-off window still honour the delay.
type RecoveryController struct {
	store     state.Storer
	bus       *eventbus.Bus
	events    *state.EventWriter
	subID     string
	decisions chan RecoveryDecision
}

// NewRecoveryController creates a RecoveryController and subscribes to the bus.
func NewRecoveryController(store state.Storer, bus *eventbus.Bus) *RecoveryController {
	rc := &RecoveryController{
		store:     store,
		bus:       bus,
		events:    state.NewEventWriter(store, state.SourceRecovery, state.ComponentNexus),
		decisions: make(chan RecoveryDecision, 20),
	}
	rc.subID = bus.Subscribe(eventbus.TopicRecoveryNeeded, rc.onRecoveryNeeded)
	return rc
}

// ── RUN ──────────────────────────────────────────────────────────────────────

// Run processes recovery decisions and blocks until ctx is cancelled.
func (rc *RecoveryController) Run(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer rc.bus.Unsubscribe(rc.subID)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			rc.processPending()
		}
	}
}

// Decisions returns a read-only channel of recovery decisions for observability.
func (rc *RecoveryController) Decisions() <-chan RecoveryDecision {
	return rc.decisions
}

// ── EVENT HANDLER ────────────────────────────────────────────────────────────

func (rc *RecoveryController) onRecoveryNeeded(event eventbus.Event) error {
	payload, ok := event.Payload.(eventbus.RecoveryPayload)
	if !ok {
		return fmt.Errorf("recovery controller: unexpected payload type for TopicRecoveryNeeded")
	}

	svc, err := rc.store.GetService(payload.ServiceID)
	if err != nil {
		return fmt.Errorf("recovery controller: get service %s: %w", payload.ServiceID, err)
	}
	if svc == nil {
		return fmt.Errorf("recovery controller: service %s not found", payload.ServiceID)
	}

	decision := rc.decide(svc)
	rc.execute(svc, decision)
	rc.publishDecision(decision)
	return nil
}

// ── POLICY ───────────────────────────────────────────────────────────────────

// decide applies recovery policy. All thresholds come from internal/config.
func (rc *RecoveryController) decide(svc *state.Service) RecoveryDecision {
	now := time.Now().UTC()

	if svc.ActualState == state.StateMaintenance {
		return RecoveryDecision{
			ServiceID: svc.ID,
			Action:    RecoveryActionSkip,
			Reason:    "service already in maintenance mode",
			DecidedAt: now,
		}
	}

	recentFailures, err := rc.store.GetRecentFailures(svc.ID, config.MaintenanceWindowMinutes)
	if err != nil {
		return RecoveryDecision{
			ServiceID:    svc.ID,
			Action:       RecoveryActionBackOff,
			BackOffDelay: config.BackOffSchedule[0],
			Reason:       fmt.Sprintf("cannot read failure count: %v — defaulting to back-off", err),
			DecidedAt:    now,
		}
	}

	if recentFailures >= config.MaintenanceFailureThreshold {
		return RecoveryDecision{
			ServiceID: svc.ID,
			Action:    RecoveryActionMaintenance,
			Reason: fmt.Sprintf(
				"%d failures in %d minutes exceeds threshold of %d",
				recentFailures, config.MaintenanceWindowMinutes, config.MaintenanceFailureThreshold,
			),
			DecidedAt: now,
		}
	}

	if svc.FailCount < len(config.BackOffSchedule) {
		delay := config.BackOffSchedule[svc.FailCount]
		return RecoveryDecision{
			ServiceID:    svc.ID,
			Action:       RecoveryActionBackOff,
			BackOffDelay: delay,
			Reason:       fmt.Sprintf("fail count %d — back-off for %s", svc.FailCount, delay),
			DecidedAt:    now,
		}
	}

	return RecoveryDecision{
		ServiceID: svc.ID,
		Action:    RecoveryActionMaintenance,
		Reason:    fmt.Sprintf("fail count %d exceeds back-off schedule", svc.FailCount),
		DecidedAt: now,
	}
}

// ── EXECUTE ──────────────────────────────────────────────────────────────────

func (rc *RecoveryController) execute(svc *state.Service, decision RecoveryDecision) {
	traceID := fmt.Sprintf("recovery-%s-%d", svc.ID, decision.DecidedAt.UnixNano())

	switch decision.Action {
	case RecoveryActionSkip:
		// nothing to do

	case RecoveryActionBackOff:
		restartAt := decision.DecidedAt.Add(decision.BackOffDelay)

		// Persist to DB — survives daemon restarts during the back-off window.
		if err := rc.store.SetRestartAfter(svc.ID, restartAt); err != nil {
			_ = rc.events.SystemAlert("warn",
				fmt.Sprintf("recovery: failed to persist restart_after for %s: %v", svc.ID, err),
				map[string]string{"service_id": svc.ID},
			)
		}

		_ = rc.events.StateChanged(svc.ID, traceID,
			string(svc.ActualState), string(state.StateRecovering))
		_ = rc.store.SetActualState(svc.ID, state.StateRecovering)

	case RecoveryActionMaintenance:
		_ = rc.store.SetActualState(svc.ID, state.StateMaintenance)
		_ = rc.store.SetDesiredState(svc.ID, state.StateStopped)

		_ = rc.events.SystemAlert("critical",
			fmt.Sprintf("service %s moved to maintenance: %s", svc.ID, decision.Reason),
			map[string]string{
				"service_id": svc.ID,
				"reason":     decision.Reason,
				"fail_count": fmt.Sprintf("%d", svc.FailCount),
			},
		)
		rc.bus.Publish(eventbus.TopicSystemAlert, svc.ID, eventbus.AlertPayload{
			Severity: "critical",
			Message:  fmt.Sprintf("%s → maintenance: %s", svc.ID, decision.Reason),
			Context:  map[string]string{"service_id": svc.ID},
		})
	}
}

// ── PENDING RESTARTS ─────────────────────────────────────────────────────────

// processPending queries the store for services whose back-off window has elapsed
// and re-queues them for startup by setting desired_state = running.
//
// Previously: iterated an in-memory map — lost on daemon kill.
// Now:        queries store.GetServicesReadyToRestart() — survives restarts.
func (rc *RecoveryController) processPending() {
	ready, err := rc.store.GetServicesReadyToRestart()
	if err != nil {
		_ = rc.events.SystemAlert("warn",
			fmt.Sprintf("recovery: failed to query services ready to restart: %v", err),
			map[string]string{"source": "recovery-controller"},
		)
		return
	}

	for _, svc := range ready {
		if err := rc.store.SetDesiredState(svc.ID, state.StateRunning); err != nil {
			_ = rc.events.SystemAlert("warn",
				fmt.Sprintf("recovery: failed to set desired state for %s: %v", svc.ID, err),
				map[string]string{"service_id": svc.ID},
			)
			continue
		}

		// Clear the restart_after so it is not picked up again next tick.
		if err := rc.store.ClearRestartAfter(svc.ID); err != nil {
			_ = rc.events.SystemAlert("warn",
				fmt.Sprintf("recovery: failed to clear restart_after for %s: %v", svc.ID, err),
				map[string]string{"service_id": svc.ID},
			)
		}
	}
}

// ── PUBLISH ──────────────────────────────────────────────────────────────────

func (rc *RecoveryController) publishDecision(decision RecoveryDecision) {
	select {
	case rc.decisions <- decision:
	default:
		select {
		case <-rc.decisions:
		default:
		}
		rc.decisions <- decision
	}
}
