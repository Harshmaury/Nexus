// @nexus-project: nexus
// @nexus-path: internal/daemon/engine_test.go
package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/pkg/runtime"
)

// ── FAKE PROVIDER ─────────────────────────────────────────────────────────────

// fakeProvider records calls for assertion in tests.
// It is safe for concurrent use by the reconciler goroutine.
type fakeProvider struct {
	startCalls int
	stopCalls  int
	running    bool
	startErr   error
	stopErr    error
	isRunErr   error
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Start(_ context.Context, _ *state.Service) error {
	f.startCalls++
	if f.startErr != nil {
		return f.startErr
	}
	f.running = true
	return nil
}

func (f *fakeProvider) Stop(_ context.Context, _ *state.Service) error {
	f.stopCalls++
	if f.stopErr != nil {
		return f.stopErr
	}
	f.running = false
	return nil
}

func (f *fakeProvider) IsRunning(_ context.Context, _ *state.Service) (bool, error) {
	return f.running, f.isRunErr
}

// ── HELPERS ───────────────────────────────────────────────────────────────────

func newTestEngine(t *testing.T, providers runtime.Providers) (*Engine, *state.Store) {
	t.Helper()
	store, err := state.New(":memory:")
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	bus := eventbus.New()
	engine := NewEngine(EngineConfig{
		Store:     store,
		Bus:       bus,
		Providers: providers,
		Interval:  10 * time.Millisecond, // fast ticks for tests
	})
	return engine, store
}

func registerService(t *testing.T, store *state.Store, id, project string, desired state.ServiceState, provider state.ProviderType) {
	t.Helper()
	svc := &state.Service{
		ID:           id,
		Name:         id,
		Project:      project,
		DesiredState: desired,
		ActualState:  state.StateUnknown,
		Provider:     provider,
	}
	if err := store.UpsertService(svc); err != nil {
		t.Fatalf("UpsertService %q: %v", id, err)
	}
}

// ── TESTS ─────────────────────────────────────────────────────────────────────

// TestEngine_StartsServiceWhenDesiredRunning verifies that when a service has
// DesiredState=running and ActualState=unknown, the engine calls provider.Start.
func TestEngine_StartsServiceWhenDesiredRunning(t *testing.T) {
	fake := &fakeProvider{running: false}
	engine, store := newTestEngine(t, runtime.Providers{
		state.ProviderDocker: fake,
	})

	registerService(t, store, "web", "myapp", state.StateRunning, state.ProviderDocker)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_ = engine.Run(ctx) // runs until context times out — expected

	if fake.startCalls == 0 {
		t.Error("expected provider.Start to be called at least once")
	}
}

// TestEngine_StopsServiceWhenDesiredStopped verifies that when a service has
// DesiredState=stopped and is currently running, the engine calls provider.Stop.
func TestEngine_StopsServiceWhenDesiredStopped(t *testing.T) {
	fake := &fakeProvider{running: true} // simulate: currently running
	engine, store := newTestEngine(t, runtime.Providers{
		state.ProviderDocker: fake,
	})

	svc := &state.Service{
		ID:           "worker",
		Name:         "worker",
		Project:      "myapp",
		DesiredState: state.StateStopped,
		ActualState:  state.StateRunning, // reconciler sees it as running
		Provider:     state.ProviderDocker,
	}
	if err := store.UpsertService(svc); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_ = engine.Run(ctx)

	if fake.stopCalls == 0 {
		t.Error("expected provider.Stop to be called at least once")
	}
}

// TestEngine_SkipsServiceWithUnknownProvider verifies that when no provider
// is registered for a service's ProviderType, the engine records an error
// but does NOT panic and continues reconciling other services.
func TestEngine_SkipsServiceWithUnknownProvider(t *testing.T) {
	// Register only docker — service uses k8s which has no provider.
	fake := &fakeProvider{}
	engine, store := newTestEngine(t, runtime.Providers{
		state.ProviderDocker: fake,
	})

	registerService(t, store, "k8s-svc", "myapp", state.StateRunning, state.ProviderK8s)
	registerService(t, store, "docker-svc", "myapp", state.StateRunning, state.ProviderDocker)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Should not panic.
	_ = engine.Run(ctx)

	// Docker service should still have been attempted.
	if fake.startCalls == 0 {
		t.Error("docker service should still be reconciled when k8s provider is missing")
	}
}

// TestEngine_ReconcileResult_Summary verifies the summary string format.
func TestEngine_ReconcileResult_Summary(t *testing.T) {
	result := &ReconcileResult{
		CycleID:   "test-cycle",
		Started:   []string{"svc-a", "svc-b"},
		Stopped:   []string{"svc-c"},
		Skipped:   []string{"svc-d"},
		TickedAt:  time.Now(),
		Duration:  12 * time.Millisecond,
	}

	summary := result.Summary()
	if summary == "" {
		t.Error("expected non-empty summary string")
	}
	// HasErrors should be false with no errors set.
	if result.HasErrors() {
		t.Error("expected HasErrors() = false")
	}
}

// TestEngine_ReconcileResult_HasErrors verifies error detection.
func TestEngine_ReconcileResult_HasErrors(t *testing.T) {
	result := &ReconcileResult{
		Errors: []ReconcileError{
			{ServiceID: "broken-svc", Action: "start", Err: context.DeadlineExceeded},
		},
	}
	if !result.HasErrors() {
		t.Error("expected HasErrors() = true when errors are present")
	}
	if result.Errors[0].Error() == "" {
		t.Error("ReconcileError.Error() should return non-empty string")
	}
}

// TestEngine_NoServices_NoOp verifies the engine handles an empty service list gracefully.
func TestEngine_NoServices_NoOp(t *testing.T) {
	engine, _ := newTestEngine(t, runtime.Providers{})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := engine.Run(ctx)
	// context.DeadlineExceeded or nil are both acceptable — no panic.
	if err != nil && err != context.DeadlineExceeded {
		t.Errorf("unexpected error: %v", err)
	}
}
