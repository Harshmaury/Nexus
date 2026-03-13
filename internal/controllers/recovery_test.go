// @nexus-project: nexus
// @nexus-path: internal/controllers/recovery_test.go
// Tests for RecoveryController — covering restart policy decisions and
// the Phase 7.3 persistence of back-off state to the store.
//
// Run with: go test ./internal/controllers/... -v
package controllers_test

import (
	"context"
	"testing"
	"time"

	"github.com/Harshmaury/Nexus/internal/config"
	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ── HELPERS ──────────────────────────────────────────────────────────────────

func openRecoveryStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.New(":memory:")
	if err != nil {
		t.Fatalf("openRecoveryStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedSvc(t *testing.T, store *state.Store, id string, failCount int, actualState state.ServiceState) *state.Service {
	t.Helper()
	svc := &state.Service{
		ID:           id,
		Name:         id,
		Project:      "test-project",
		DesiredState: state.StateRunning,
		ActualState:  actualState,
		Provider:     state.ProviderDocker,
		Config:       "{}",
		FailCount:    failCount,
	}
	if err := store.UpsertService(svc); err != nil {
		t.Fatalf("seedSvc: %v", err)
	}
	return svc
}

func fireRecoveryEvent(t *testing.T, bus *eventbus.Bus, serviceID string) {
	t.Helper()
	bus.Publish(eventbus.TopicRecoveryNeeded, serviceID, eventbus.RecoveryPayload{
		ServiceID:  serviceID,
		FailCount:  0,
		LastFailed: time.Now(),
	})
}

// ── POLICY DECISION TESTS ────────────────────────────────────────────────────

func TestRecoveryController_FirstFailure_DecisionsBackOff(t *testing.T) {
	store := openRecoveryStore(t)
	bus := eventbus.New()
	rc := controllers.NewRecoveryController(store, bus)

	seedSvc(t, store, "svc-first-fail", 0, state.StateCrashed)
	fireRecoveryEvent(t, bus, "svc-first-fail")

	var decision controllers.RecoveryDecision
	select {
	case decision = <-rc.Decisions():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for recovery decision")
	}

	if decision.Action != controllers.RecoveryActionBackOff {
		t.Errorf("Action = %q, want %q", decision.Action, controllers.RecoveryActionBackOff)
	}
	if decision.BackOffDelay != config.BackOffSchedule[0] {
		t.Errorf("BackOffDelay = %s, want %s", decision.BackOffDelay, config.BackOffSchedule[0])
	}
}

func TestRecoveryController_SecondFailure_BackOffUsesSecondScheduleSlot(t *testing.T) {
	store := openRecoveryStore(t)
	bus := eventbus.New()
	rc := controllers.NewRecoveryController(store, bus)

	seedSvc(t, store, "svc-second-fail", 1, state.StateCrashed)
	fireRecoveryEvent(t, bus, "svc-second-fail")

	var decision controllers.RecoveryDecision
	select {
	case decision = <-rc.Decisions():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for recovery decision")
	}

	if decision.Action != controllers.RecoveryActionBackOff {
		t.Errorf("Action = %q, want back-off", decision.Action)
	}
	if decision.BackOffDelay != config.BackOffSchedule[1] {
		t.Errorf("BackOffDelay = %s, want %s", decision.BackOffDelay, config.BackOffSchedule[1])
	}
}

func TestRecoveryController_ExceedingBackOffSchedule_EscalatesToMaintenance(t *testing.T) {
	store := openRecoveryStore(t)
	bus := eventbus.New()
	rc := controllers.NewRecoveryController(store, bus)

	failCount := len(config.BackOffSchedule) + 1
	seedSvc(t, store, "svc-beyond-schedule", failCount, state.StateCrashed)
	fireRecoveryEvent(t, bus, "svc-beyond-schedule")

	var decision controllers.RecoveryDecision
	select {
	case decision = <-rc.Decisions():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for recovery decision")
	}

	if decision.Action != controllers.RecoveryActionMaintenance {
		t.Errorf("Action = %q, want maintenance", decision.Action)
	}
}

func TestRecoveryController_ServiceAlreadyInMaintenance_DecisionsSkip(t *testing.T) {
	store := openRecoveryStore(t)
	bus := eventbus.New()
	rc := controllers.NewRecoveryController(store, bus)

	seedSvc(t, store, "svc-maintenance", 0, state.StateMaintenance)
	fireRecoveryEvent(t, bus, "svc-maintenance")

	var decision controllers.RecoveryDecision
	select {
	case decision = <-rc.Decisions():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for recovery decision")
	}

	if decision.Action != controllers.RecoveryActionSkip {
		t.Errorf("Action = %q, want skip", decision.Action)
	}
}

// ── BACK-OFF PERSISTENCE TESTS (Phase 7.3) ────────────────────────────────────

func TestRecoveryController_BackOff_PersistsRestartAfterToStore(t *testing.T) {
	store := openRecoveryStore(t)
	bus := eventbus.New()
	_ = controllers.NewRecoveryController(store, bus)

	seedSvc(t, store, "svc-backoff-persist", 0, state.StateCrashed)
	fireRecoveryEvent(t, bus, "svc-backoff-persist")

	time.Sleep(50 * time.Millisecond)

	got, err := store.GetService("svc-backoff-persist")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if got.RestartAfter == nil {
		t.Fatal("RestartAfter = nil after back-off — must be persisted to store")
	}
	if got.RestartAfter.Before(time.Now()) {
		t.Errorf("RestartAfter = %v is in the past — back-off delay was not applied", got.RestartAfter)
	}
}

func TestRecoveryController_ProcessPending_RequeuesServicesWhoseWindowElapsed(t *testing.T) {
	store := openRecoveryStore(t)
	bus := eventbus.New()
	rc := controllers.NewRecoveryController(store, bus)

	// Seed with desired=stopped so we can clearly observe processPending setting it to running.
	svc := &state.Service{
		ID: "svc-ready-to-restart", Name: "svc-ready-to-restart", Project: "test",
		DesiredState: state.StateStopped, // will be changed to running by processPending
		ActualState:  state.StateRecovering,
		Provider:     state.ProviderDocker, Config: "{}",
	}
	if err := store.UpsertService(svc); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	if err := store.SetRestartAfter(svc.ID, time.Now().Add(-1*time.Second)); err != nil {
		t.Fatalf("SetRestartAfter: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = rc.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.GetService(svc.ID)
		if got.DesiredState == state.StateRunning && got.RestartAfter == nil {
			cancel()
			return // success
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, _ := store.GetService(svc.ID)
	t.Errorf("expected desired=running and restart_after=nil; got desired=%s restart_after=%v",
		got.DesiredState, got.RestartAfter)
}

func TestRecoveryController_ProcessPending_DoesNotRequeueServicesStillInWindow(t *testing.T) {
	store := openRecoveryStore(t)
	bus := eventbus.New()
	rc := controllers.NewRecoveryController(store, bus)

	// Seed with desired=stopped — if processPending fires incorrectly it would change this to running.
	svc := &state.Service{
		ID: "svc-still-waiting", Name: "svc-still-waiting", Project: "test",
		DesiredState: state.StateStopped, // must remain stopped throughout the test
		ActualState:  state.StateRecovering,
		Provider:     state.ProviderDocker, Config: "{}",
	}
	if err := store.UpsertService(svc); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	// restart_after is 5 minutes in the future — well outside the processPending window.
	if err := store.SetRestartAfter(svc.ID, time.Now().Add(5*time.Minute)); err != nil {
		t.Fatalf("SetRestartAfter: %v", err)
	}

	// Run for just over one processPending tick (ticker = 2s).
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	go func() { _ = rc.Run(ctx) }()
	<-ctx.Done()

	got, _ := store.GetService(svc.ID)

	// desired state must still be stopped — processPending must not have touched it.
	if got.DesiredState == state.StateRunning {
		t.Error("service was re-queued before its back-off window elapsed — it must not be")
	}
	// restart_after must still be set — ClearRestartAfter must not have been called.
	if got.RestartAfter == nil {
		t.Error("restart_after was cleared before the back-off window elapsed")
	}
}
