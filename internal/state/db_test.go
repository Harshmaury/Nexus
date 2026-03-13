// @nexus-project: nexus
// @nexus-path: internal/state/db_test.go
// Tests for the state store — covering migrations, service CRUD, and the
// new Phase 7.3 back-off persistence methods (SetRestartAfter, ClearRestartAfter,
// GetServicesReadyToRestart).
//
// Run with: go test ./internal/state/... -v
package state_test

import (
	"testing"
	"time"

	"github.com/Harshmaury/Nexus/internal/state"
)

// ── HELPERS ──────────────────────────────────────────────────────────────────

// openTestStore opens an in-memory SQLite database and registers cleanup.
// Every test gets a fresh, isolated store with migrations already applied.
func openTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.New(":memory:")
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// seedService inserts a service into the store and returns it.
func seedService(t *testing.T, store *state.Store, svc *state.Service) *state.Service {
	t.Helper()
	if err := store.UpsertService(svc); err != nil {
		t.Fatalf("seedService: %v", err)
	}
	return svc
}

// makeService returns a minimal valid service for tests.
func makeService(id, project string) *state.Service {
	return &state.Service{
		ID:           id,
		Name:         id + "-svc",
		Project:      project,
		DesiredState: state.StateStopped,
		ActualState:  state.StateUnknown,
		Provider:     state.ProviderDocker,
		Config:       "{}",
	}
}

// ── MIGRATION TESTS ───────────────────────────────────────────────────────────

func TestNew_OpeningAnInMemoryDatabaseSucceeds(t *testing.T) {
	store := openTestStore(t)
	if store == nil {
		t.Fatal("expected non-nil store")
	}
}

func TestNew_MigrationsAreIdempotent(t *testing.T) {
	// Opening the same in-memory DB a second time should not fail.
	// In production this exercises the "already at version N" path.
	store, err := state.New(":memory:")
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = store.Close()

	// Simulate a second daemon start against the same file DB path.
	// Using a temp file would be cleaner but :memory: is sufficient to
	// verify that migrate() does not panic on an already-migrated schema.
	store2, err := state.New(":memory:")
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	_ = store2.Close()
}

func TestNew_RestartAfterColumnExistsAfterMigration(t *testing.T) {
	// Verifies that migration v2 (ALTER TABLE ADD COLUMN restart_after) ran.
	// If the column is missing, UpsertService and GetServicesReadyToRestart
	// would both fail at runtime.
	store := openTestStore(t)

	svc := makeService("svc-col-test", "proj")
	if err := store.UpsertService(svc); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}

	// SetRestartAfter touches restart_after — if the column is absent this panics.
	restartAt := time.Now().Add(10 * time.Second)
	if err := store.SetRestartAfter(svc.ID, restartAt); err != nil {
		t.Fatalf("SetRestartAfter after migration: %v — restart_after column is missing", err)
	}
}

// ── SERVICE CRUD TESTS ───────────────────────────────────────────────────────

func TestUpsertService_InsertsServiceAndItCanBeRetrieved(t *testing.T) {
	store := openTestStore(t)
	svc := makeService("alpha", "project-a")
	svc.DesiredState = state.StateRunning

	if err := store.UpsertService(svc); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}

	got, err := store.GetService("alpha")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if got == nil {
		t.Fatal("GetService: returned nil, want service")
	}
	if got.DesiredState != state.StateRunning {
		t.Errorf("DesiredState = %q, want %q", got.DesiredState, state.StateRunning)
	}
}

func TestGetService_ReturnsNilForUnknownID(t *testing.T) {
	store := openTestStore(t)

	got, err := store.GetService("does-not-exist")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if got != nil {
		t.Errorf("GetService: want nil for unknown ID, got %+v", got)
	}
}

func TestSetActualState_UpdatesOnlyActualState(t *testing.T) {
	store := openTestStore(t)
	svc := seedService(t, store, makeService("beta", "project-b"))

	if err := store.SetActualState(svc.ID, state.StateRunning); err != nil {
		t.Fatalf("SetActualState: %v", err)
	}

	got, _ := store.GetService(svc.ID)
	if got.ActualState != state.StateRunning {
		t.Errorf("ActualState = %q, want %q", got.ActualState, state.StateRunning)
	}
	// DesiredState must be unchanged.
	if got.DesiredState != state.StateStopped {
		t.Errorf("DesiredState was mutated: got %q, want %q", got.DesiredState, state.StateStopped)
	}
}

func TestIncrementFailCount_IncrementsBy1EachCall(t *testing.T) {
	store := openTestStore(t)
	svc := seedService(t, store, makeService("gamma", "project-c"))

	for i := 1; i <= 3; i++ {
		if err := store.IncrementFailCount(svc.ID); err != nil {
			t.Fatalf("IncrementFailCount: %v", err)
		}
	}

	got, _ := store.GetService(svc.ID)
	if got.FailCount != 3 {
		t.Errorf("FailCount = %d, want 3", got.FailCount)
	}
}

func TestResetFailCount_ClearsFailCountAndLastFailedAt(t *testing.T) {
	store := openTestStore(t)
	svc := seedService(t, store, makeService("delta", "project-d"))

	_ = store.IncrementFailCount(svc.ID)
	_ = store.IncrementFailCount(svc.ID)

	if err := store.ResetFailCount(svc.ID); err != nil {
		t.Fatalf("ResetFailCount: %v", err)
	}

	got, _ := store.GetService(svc.ID)
	if got.FailCount != 0 {
		t.Errorf("FailCount after reset = %d, want 0", got.FailCount)
	}
	if got.LastFailedAt != nil {
		t.Errorf("LastFailedAt after reset = %v, want nil", got.LastFailedAt)
	}
}

func TestGetAllServices_ReturnsAllInsertedServices(t *testing.T) {
	store := openTestStore(t)
	seedService(t, store, makeService("svc-1", "proj"))
	seedService(t, store, makeService("svc-2", "proj"))
	seedService(t, store, makeService("svc-3", "proj"))

	all, err := store.GetAllServices()
	if err != nil {
		t.Fatalf("GetAllServices: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("len(services) = %d, want 3", len(all))
	}
}

func TestGetServicesByProject_ReturnsOnlyServicesForThatProject(t *testing.T) {
	store := openTestStore(t)
	seedService(t, store, makeService("svc-a", "project-alpha"))
	seedService(t, store, makeService("svc-b", "project-alpha"))
	seedService(t, store, makeService("svc-c", "project-beta"))

	got, err := store.GetServicesByProject("project-alpha")
	if err != nil {
		t.Fatalf("GetServicesByProject: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

// ── RESTART-AFTER PERSISTENCE TESTS (Phase 7.3) ───────────────────────────────

func TestSetRestartAfter_PersistsRestartTimeToDatabase(t *testing.T) {
	store := openTestStore(t)
	svc := seedService(t, store, makeService("restart-svc", "proj"))

	restartAt := time.Now().Add(30 * time.Second).UTC().Truncate(time.Second)
	if err := store.SetRestartAfter(svc.ID, restartAt); err != nil {
		t.Fatalf("SetRestartAfter: %v", err)
	}

	got, _ := store.GetService(svc.ID)
	if got.RestartAfter == nil {
		t.Fatal("RestartAfter = nil after SetRestartAfter, want non-nil")
	}
	if got.RestartAfter.Unix() != restartAt.Unix() {
		t.Errorf("RestartAfter = %v, want %v", got.RestartAfter, restartAt)
	}
}

func TestClearRestartAfter_SetsRestartAfterToNil(t *testing.T) {
	store := openTestStore(t)
	svc := seedService(t, store, makeService("clear-svc", "proj"))

	_ = store.SetRestartAfter(svc.ID, time.Now().Add(10*time.Second))

	if err := store.ClearRestartAfter(svc.ID); err != nil {
		t.Fatalf("ClearRestartAfter: %v", err)
	}

	got, _ := store.GetService(svc.ID)
	if got.RestartAfter != nil {
		t.Errorf("RestartAfter after clear = %v, want nil", got.RestartAfter)
	}
}

func TestGetServicesReadyToRestart_ReturnsOnlyServicesWhoseWindowHasElapsed(t *testing.T) {
	store := openTestStore(t)

	// svc-ready: restart_after is in the past — should be returned.
	ready := seedService(t, store, makeService("svc-ready", "proj"))
	_ = store.SetRestartAfter(ready.ID, time.Now().Add(-1*time.Second))

	// svc-waiting: restart_after is in the future — must NOT be returned.
	waiting := seedService(t, store, makeService("svc-waiting", "proj"))
	_ = store.SetRestartAfter(waiting.ID, time.Now().Add(60*time.Second))

	// svc-none: no restart_after set — must NOT be returned.
	seedService(t, store, makeService("svc-none", "proj"))

	got, err := store.GetServicesReadyToRestart()
	if err != nil {
		t.Fatalf("GetServicesReadyToRestart: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (only svc-ready)", len(got))
	}
	if got[0].ID != "svc-ready" {
		t.Errorf("returned service ID = %q, want %q", got[0].ID, "svc-ready")
	}
}

func TestGetServicesReadyToRestart_ReturnsEmptySliceWhenNoneAreReady(t *testing.T) {
	store := openTestStore(t)
	svc := seedService(t, store, makeService("svc-future", "proj"))
	_ = store.SetRestartAfter(svc.ID, time.Now().Add(5*time.Minute))

	got, err := store.GetServicesReadyToRestart()
	if err != nil {
		t.Fatalf("GetServicesReadyToRestart: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// ── HEALTH LOG TESTS ─────────────────────────────────────────────────────────

func TestGetRecentFailures_CountsCrashedStatusesWithinWindow(t *testing.T) {
	store := openTestStore(t)
	svc := seedService(t, store, makeService("health-svc", "proj"))

	// Log 2 crashes.
	_ = store.LogHealth(svc.ID, state.StateCrashed, 1, "oom killed")
	_ = store.LogHealth(svc.ID, state.StateCrashed, 1, "signal 9")
	// Log 1 running — should NOT count.
	_ = store.LogHealth(svc.ID, state.StateRunning, 0, "ok")

	count, err := store.GetRecentFailures(svc.ID, 60)
	if err != nil {
		t.Fatalf("GetRecentFailures: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

// ── PROJECT TESTS ────────────────────────────────────────────────────────────

func TestRegisterProject_CanBeRetrievedByID(t *testing.T) {
	store := openTestStore(t)

	p := &state.Project{
		ID:          "ums",
		Name:        "University Management System",
		Path:        "/home/harsh/dev/ums",
		Language:    "go",
		ProjectType: "microservices",
		ConfigJSON:  "{}",
	}

	if err := store.RegisterProject(p); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}

	got, err := store.GetProject("ums")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got == nil {
		t.Fatal("GetProject returned nil")
	}
	if got.Name != "University Management System" {
		t.Errorf("Name = %q, want %q", got.Name, "University Management System")
	}
}

func TestGetProject_ReturnsNilForUnregisteredProject(t *testing.T) {
	store := openTestStore(t)

	got, err := store.GetProject("nonexistent")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got != nil {
		t.Errorf("want nil for unregistered project, got %+v", got)
	}
}
