// @nexus-project: nexus
// @nexus-path: internal/controllers/recovery.go
// RecoveryController listens for TopicRecoveryNeeded events from the health controller
// and decides whether to attempt a restart or escalate to maintenance mode.
// This is where all restart policy lives — the reconciler and health controller
// are policy-free. Only RecoveryController makes recovery decisions.
package controllers

import (
	"fmt"
	"sync"
	"time"

	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	// A service that crashes this many times within failureWindowMinutes
	// is moved to maintenance mode instead of being restarted again.
	maintenanceFailureThreshold = 3
	maintenanceWindowMinutes    = 60

	// Back-off delays between restart attempts.
	// Attempt 1 → 5s, Attempt 2 → 15s, Attempt 3 → 30s, then maintenance.
	backOffAttempt1 = 5 * time.Second
	backOffAttempt2 = 15 * time.Second
	backOffAttempt3 = 30 * time.Second
)

// backOffSchedule maps fail count to the delay before the next restart attempt.
// FailCount 0 = first failure, get immediate short delay.
// Beyond the schedule = maintenance.
var backOffSchedule = []time.Duration{
	backOffAttempt1,
	backOffAttempt2,
	backOffAttempt3,
}

// ── RECOVERY DECISION ────────────────────────────────────────────────────────

// RecoveryAction is the decision the controller makes for a crashed service.
type RecoveryAction string

const (
	RecoveryActionRestart     RecoveryAction = "restart"
	RecoveryActionBackOff     RecoveryAction = "back-off"
	RecoveryActionMaintenance RecoveryAction = "maintenance"
	RecoveryActionSkip        RecoveryAction = "skip" // already in maintenance
)

// RecoveryDecision is the full structured decision for one service.
type RecoveryDecision struct {
	ServiceID    string
	Action       RecoveryAction
	BackOffDelay time.Duration  // non-zero when action = back-off
	Reason       string
	DecidedAt    time.Time
}

// ── RECOVERY CONTROLLER ──────────────────────────────────────────────────────

// RecoveryController subscribes to crash events and enforces restart policy.
// It sets desired_state back to running after a back-off delay,
// or moves the service to maintenance if the threshold is exceeded.
//
// Policy owned here (moved from reconciler TODO):
//   - Back-off schedule per fail count
//   - Maintenance threshold: 3 failures in 60 minutes
type RecoveryController struct {
	store     *state.Store
	bus       *eventbus.Bus
	events    *state.EventWriter
	subID     string // bus subscription ID for cleanup
	decisions chan RecoveryDecision

	// pending tracks services waiting for back-off delay.
	// key = serviceID, value = time when restart is allowed.
	mu      sync.Mutex
	pending map[string]time.Time
}

// NewRecoveryController creates a RecoveryController and subscribes to the bus.
func NewRecoveryController(store *state.Store, bus *eventbus.Bus) *RecoveryController {
	rc := &RecoveryController{
		store:     store,
		bus:       bus,
		events:    state.NewEventWriter(store, state.SourceRecovery),
		decisions: make(chan RecoveryDecision, 20),
		pending:   make(map[string]time.Time),
	}

	// Subscribe to recovery needed events from the health controller.
	rc.subID = bus.Subscribe(eventbus.TopicRecoveryNeeded, rc.onRecoveryNeeded)
	return rc
}

// ── RUN ──────────────────────────────────────────────────────────────────────

// Run processes recovery decisions and blocks until ctx is cancelled.
// Call in a goroutine from the daemon entry point.
// It also runs a ticker to process pending back-off restarts.
func (rc *RecoveryController) Run(ctx <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer rc.bus.Unsubscribe(rc.subID)

	for {
		select {
		case <-ctx:
			return
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

// onRecoveryNeeded is called by the event bus when a service crashes.
// It evaluates policy and schedules the appropriate action.
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

// decide applies recovery policy and returns the appropriate action.
// This is the single place where restart policy is expressed.
func (rc *RecoveryController) decide(svc *state.Service) RecoveryDecision {
	now := time.Now().UTC()
	tracePrefix := fmt.Sprintf("recovery-%s", svc.ID)
	_ = tracePrefix // used in execute

	// Already in maintenance — nothing to do.
	if svc.ActualState == state.StateMaintenance {
		return RecoveryDecision{
			ServiceID: svc.ID,
			Action:    RecoveryActionSkip,
			Reason:    "service already in maintenance mode",
			DecidedAt: now,
		}
	}

	// Check recent failure count from health logs.
	recentFailures, err := rc.store.GetRecentFailures(svc.ID, maintenanceWindowMinutes)
	if err != nil {
		// Cannot determine failure count — safe default is back-off.
		return RecoveryDecision{
			ServiceID:    svc.ID,
			Action:       RecoveryActionBackOff,
			BackOffDelay: backOffAttempt1,
			Reason:       fmt.Sprintf("cannot read failure count: %v — defaulting to back-off", err),
			DecidedAt:    now,
		}
	}

	// Too many failures — escalate to maintenance.
	if recentFailures >= maintenanceFailureThreshold {
		return RecoveryDecision{
			ServiceID: svc.ID,
			Action:    RecoveryActionMaintenance,
			Reason: fmt.Sprintf(
				"%d failures in %d minutes exceeds threshold of %d",
				recentFailures, maintenanceWindowMinutes, maintenanceFailureThreshold,
			),
			DecidedAt: now,
		}
	}

	// Apply back-off schedule based on total fail count.
	if svc.FailCount < len(backOffSchedule) {
		delay := backOffSchedule[svc.FailCount]
		return RecoveryDecision{
			ServiceID:    svc.ID,
			Action:       RecoveryActionBackOff,
			BackOffDelay: delay,
			Reason:       fmt.Sprintf("fail count %d — back-off for %s", svc.FailCount, delay),
			DecidedAt:    now,
		}
	}

	// Fail count beyond schedule — maintenance.
	return RecoveryDecision{
		ServiceID: svc.ID,
		Action:    RecoveryActionMaintenance,
		Reason:    fmt.Sprintf("fail count %d exceeds back-off schedule", svc.FailCount),
		DecidedAt: now,
	}
}

// ── EXECUTE ──────────────────────────────────────────────────────────────────

// execute acts on a recovery decision.
func (rc *RecoveryController) execute(svc *state.Service, decision RecoveryDecision) {
	traceID := fmt.Sprintf("recovery-%s-%d", svc.ID, decision.DecidedAt.UnixNano())

	switch decision.Action {

	case RecoveryActionSkip:
		// Nothing to do.

	case RecoveryActionBackOff:
		// Schedule a restart after the back-off delay.
		// The reconciler will pick it up once desired_state is set back to running.
		restartAt := decision.DecidedAt.Add(decision.BackOffDelay)
		rc.mu.Lock()
		rc.pending[svc.ID] = restartAt
		rc.mu.Unlock()

		_ = rc.events.StateChanged(
			svc.ID, traceID,
			string(svc.ActualState),
			string(state.StateRecovering),
		)
		_ = rc.store.SetActualState(svc.ID, state.StateRecovering)

	case RecoveryActionMaintenance:
		_ = rc.store.SetActualState(svc.ID, state.StateMaintenance)
		_ = rc.store.SetDesiredState(svc.ID, state.StateStopped)

		_ = rc.events.SystemAlert(
			"critical",
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
			Context: map[string]string{
				"service_id": svc.ID,
				"fail_count": fmt.Sprintf("%d", svc.FailCount),
			},
		})
	}
}

// ── PENDING RESTARTS ─────────────────────────────────────────────────────────

// processPending checks if any back-off delays have elapsed
// and sets desired_state = running so the reconciler picks them up.
func (rc *RecoveryController) processPending() {
	now := time.Now().UTC()

	rc.mu.Lock()
	defer rc.mu.Unlock()

	for serviceID, restartAt := range rc.pending {
		if now.Before(restartAt) {
			continue // still waiting
		}

		// Back-off elapsed — allow reconciler to restart.
		if err := rc.store.SetDesiredState(serviceID, state.StateRunning); err != nil {
			_ = rc.events.SystemAlert("warn",
				fmt.Sprintf("recovery: failed to set desired state for %s: %v", serviceID, err),
				map[string]string{"service_id": serviceID},
			)
			continue
		}

		delete(rc.pending, serviceID)
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
