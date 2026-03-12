// @nexus-project: nexus
// @nexus-path: internal/state/db_test.go
package state

import (
	"testing"
	"time"
)

// ── HELPERS ───────────────────────────────────────────────────────────────────

// newTestStore creates an in-memory SQLite store for tests.
// It is closed automatically via t.Cleanup.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(":memory:")
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testService(id, project string) *Service {
	return &Service{
		ID:           id,
		Name:         id,
		Project:      project,
		DesiredState: StateStopped,
		ActualState:  StateUnknown,
		Provider:     ProviderDocker,
		Config:       `{"image":"nginx:latest"}`,
	}
}

// ── UPSERT / GET SERVICE ─────────────────────────────────────────────────────

func TestStore_UpsertAndGetService(t *testing.T) {
	store := newTestStore(t)

	svc := testService("identity-api", "ums")
	if err := store.UpsertService(svc); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}

	got, err := store.GetService("identity-api")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}

	if got.ID != svc.ID {
		t.Errorf("ID = %q, want %q", got.ID, svc.ID)
	}
	if got.Provider != ProviderDocker {
		t.Errorf("Provider = %q, want %q", got.Provider, ProviderDocker)
	}
	if got.DesiredState != StateStopped {
		t.Errorf("DesiredState = %q, want %q", got.DesiredState, StateStopped)
	}
}

func TestStore_UpsertService_Idempotent(t *testing.T) {
	store := newTestStore(t)
	svc := testService("academic-api", "ums")

	if err := store.UpsertService(svc); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	svc.Config = `{"image":"nginx:1.25"}`
	if err := store.UpsertService(svc); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := store.GetService("academic-api")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if got.Config != `{"image":"nginx:1.25"}` {
		t.Errorf("Config not updated: got %q", got.Config)
	}
}

// ── SET STATES ────────────────────────────────────────────────────────────────

func TestStore_SetDesiredState(t *testing.T) {
	store := newTestStore(t)
	store.UpsertService(testService("svc-a", "proj"))

	if err := store.SetDesiredState("svc-a", StateRunning); err != nil {
		t.Fatalf("SetDesiredState: %v", err)
	}

	got, _ := store.GetService("svc-a")
	if got.DesiredState != StateRunning {
		t.Errorf("DesiredState = %q, want running", got.DesiredState)
	}
}

func TestStore_SetActualState(t *testing.T) {
	store := newTestStore(t)
	store.UpsertService(testService("svc-b", "proj"))

	if err := store.SetActualState("svc-b", StateCrashed); err != nil {
		t.Fatalf("SetActualState: %v", err)
	}

	got, _ := store.GetService("svc-b")
	if got.ActualState != StateCrashed {
		t.Errorf("ActualState = %q, want crashed", got.ActualState)
	}
}

// ── FAIL COUNT ───────────────────────────────────────────────────────────────

func TestStore_FailCount(t *testing.T) {
	store := newTestStore(t)
	store.UpsertService(testService("svc-c", "proj"))

	for i := 0; i < 3; i++ {
		if err := store.IncrementFailCount("svc-c"); err != nil {
			t.Fatalf("IncrementFailCount iter %d: %v", i, err)
		}
	}

	got, _ := store.GetService("svc-c")
	if got.FailCount != 3 {
		t.Errorf("FailCount = %d, want 3", got.FailCount)
	}

	if err := store.ResetFailCount("svc-c"); err != nil {
		t.Fatalf("ResetFailCount: %v", err)
	}
	got, _ = store.GetService("svc-c")
	if got.FailCount != 0 {
		t.Errorf("FailCount after reset = %d, want 0", got.FailCount)
	}
}

// ── GET ALL SERVICES ─────────────────────────────────────────────────────────

func TestStore_GetAllServices(t *testing.T) {
	store := newTestStore(t)

	ids := []string{"alpha", "beta", "gamma"}
	for _, id := range ids {
		store.UpsertService(testService(id, "test-project"))
	}

	all, err := store.GetAllServices()
	if err != nil {
		t.Fatalf("GetAllServices: %v", err)
	}
	if len(all) != len(ids) {
		t.Errorf("got %d services, want %d", len(all), len(ids))
	}
}

// ── EVENTS ───────────────────────────────────────────────────────────────────

func TestStore_AppendAndGetEvents(t *testing.T) {
	store := newTestStore(t)
	store.UpsertService(testService("event-svc", "proj"))

	traceID := "trace-abc-123"
	if err := store.AppendEvent("event-svc", EventServiceStarted, SourceDaemon, traceID, ""); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := store.AppendEvent("event-svc", EventServiceStopped, SourceCLI, traceID, "{}"); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	events, err := store.GetRecentEvents(10)
	if err != nil {
		t.Fatalf("GetRecentEvents: %v", err)
	}
	if len(events) < 2 {
		t.Errorf("got %d events, want at least 2", len(events))
	}

	byTrace, err := store.GetEventsByTrace(traceID)
	if err != nil {
		t.Fatalf("GetEventsByTrace: %v", err)
	}
	if len(byTrace) != 2 {
		t.Errorf("got %d events by trace, want 2", len(byTrace))
	}
}

// ── PROJECTS ─────────────────────────────────────────────────────────────────

func TestStore_RegisterAndGetProject(t *testing.T) {
	store := newTestStore(t)

	proj := &Project{
		ID:          "nexus",
		Name:        "Nexus",
		Path:        "~/dev/nexus",
		Language:    "go",
		ProjectType: "daemon",
		ConfigJSON:  `{"name":"nexus"}`,
	}
	if err := store.RegisterProject(proj); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}

	got, err := store.GetProject("nexus")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Name != "Nexus" {
		t.Errorf("Name = %q, want Nexus", got.Name)
	}
	if got.Language != "go" {
		t.Errorf("Language = %q, want go", got.Language)
	}
}

// ── DOWNLOAD LOG ─────────────────────────────────────────────────────────────

func TestStore_LogAndGetDownloads(t *testing.T) {
	store := newTestStore(t)

	entry := &DownloadLog{
		OriginalName: "nexus__engine__20260312.go",
		RenamedTo:    "nexus__engine__20260312_1430.go",
		Project:      "nexus",
		Source:       "/mnt/c/Users/harsh/Downloads",
		Destination:  "~/dev/nexus/internal/daemon/",
		Method:       "prefix+header",
		Action:       "moved",
		Confidence:   0.95,
		DownloadedAt: time.Now().UTC(),
	}
	if err := store.LogDownload(entry); err != nil {
		t.Fatalf("LogDownload: %v", err)
	}

	logs, err := store.GetRecentDownloads(10)
	if err != nil {
		t.Fatalf("GetRecentDownloads: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("expected at least 1 download log entry")
	}

	got := logs[0]
	if got.OriginalName != entry.OriginalName {
		t.Errorf("OriginalName = %q, want %q", got.OriginalName, entry.OriginalName)
	}
	if got.Action != "moved" {
		t.Errorf("Action = %q, want moved", got.Action)
	}
	if got.Confidence != entry.Confidence {
		t.Errorf("Confidence = %.2f, want %.2f", got.Confidence, entry.Confidence)
	}
}

// ── GET NONEXISTENT ──────────────────────────────────────────────────────────

func TestStore_GetService_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetService("does-not-exist")
	if err == nil {
		t.Error("expected error for missing service, got nil")
	}
}
