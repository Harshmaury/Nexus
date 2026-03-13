// @nexus-project: nexus
// @nexus-path: internal/daemon/engine_test.go
// Tests for the reconciler engine — verifying that desired vs actual state
// is driven to convergence using a fake provider that never touches Docker/K8s.
//
// Run with: go test ./internal/daemon/... -v
package daemon_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/pkg/runtime"
)

// ── FAKE PROVIDER ─────────────────────────────────────────────────────────────

type fakeProvider struct {
	mu       sync.Mutex
	running  map[string]bool
	failNext bool
	started  []string
	stopped  []string
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{running: make(map[string]bool)}
}

// Name returns string — matches the Provider interface exactly.
func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Start(_ context.Context, svc *state.Service) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return fmt.Errorf("fakeProvider: simulated start failure")
	}
	f.running[svc.ID] = true
	f.started = append(f.started, svc.ID)
	return nil
}

func (f *fakeProvider) Stop(_ context.Context, svc *state.Service) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return fmt.Errorf("fakeProvider: simulated stop failure")
	}
	delete(f.running, svc.ID)
	f.stopped = append(f.stopped, svc.ID)
	return nil
}

func (f *fakeProvider) IsRunning(_ context.Context, svc *state.Service) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[svc.ID], nil
}

// ── HELPERS ──────────────────────────────────────────────────────────────────

func openEngineStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.New(":memory:")
	if err != nil {
		t.Fatalf("openEngineStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func runOneCycle(t *testing.T, store *state.Store, provider *fakeProvider) daemon.ReconcileResult {
	t.Helper()
	bus := eventbus.New()
	providers := runtime.Providers{state.ProviderDocker: provider}

	eng := daemon.NewEngine(daemon.EngineConfig{
		Store:     store,
		Bus:       bus,
		Providers: providers,
		Interval:  time.Hour,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var result daemon.ReconcileResult
	done := make(chan struct{})
	go func() {
		_ = eng.Run(ctx)
		close(done)
	}()

	select {
	case result = <-eng.Results():
		cancel()
	case <-ctx.Done():
		t.Fatal("timed out waiting for reconcile result")
	}

	<-done
	return result
}

// ── TESTS ─────────────────────────────────────────────────────────────────────

func TestEngine_ReconcileStartsServicesWithDesiredStateRunning(t *testing.T) {
	store := openEngineStore(t)
	provider := newFakeProvider()

	_ = store.UpsertService(&state.Service{
		ID: "api-gateway", Name: "api-gateway", Project: "ums",
		DesiredState: state.StateRunning, ActualState: state.StateStopped,
		Provider: state.ProviderDocker, Config: "{}",
	})

	result := runOneCycle(t, store, provider)

	if len(result.Started) != 1 || result.Started[0] != "api-gateway" {
		t.Errorf("Started = %v, want [api-gateway]", result.Started)
	}
	if result.HasErrors() {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
}

func TestEngine_SkipsServicesAlreadyInCorrectState(t *testing.T) {
	store := openEngineStore(t)
	provider := newFakeProvider()
	provider.running["identity-svc"] = true

	_ = store.UpsertService(&state.Service{
		ID: "identity-svc", Name: "identity-svc", Project: "ums",
		DesiredState: state.StateRunning, ActualState: state.StateRunning,
		Provider: state.ProviderDocker, Config: "{}",
	})

	result := runOneCycle(t, store, provider)

	if len(result.Started) != 0 {
		t.Errorf("Started = %v, want empty (already running)", result.Started)
	}
	if len(result.Skipped) != 1 {
		t.Errorf("Skipped = %v, want [identity-svc]", result.Skipped)
	}
}

func TestEngine_SkipsServicesInMaintenanceMode(t *testing.T) {
	store := openEngineStore(t)

	_ = store.UpsertService(&state.Service{
		ID: "broken-svc", Name: "broken-svc", Project: "ums",
		DesiredState: state.StateRunning, ActualState: state.StateMaintenance,
		Provider: state.ProviderDocker, Config: "{}",
	})

	provider := newFakeProvider()
	result := runOneCycle(t, store, provider)

	if len(result.Started) != 0 {
		t.Errorf("Started = %v, want empty (maintenance skips provider)", result.Started)
	}
	if len(provider.started) != 0 {
		t.Errorf("provider.Start was called for a maintenance service")
	}
}

func TestEngine_StopsServicesWithDesiredStateStopped(t *testing.T) {
	store := openEngineStore(t)
	provider := newFakeProvider()
	provider.running["kafka"] = true

	_ = store.UpsertService(&state.Service{
		ID: "kafka", Name: "kafka", Project: "ums",
		DesiredState: state.StateStopped, ActualState: state.StateRunning,
		Provider: state.ProviderDocker, Config: "{}",
	})

	result := runOneCycle(t, store, provider)

	if len(result.Stopped) != 1 || result.Stopped[0] != "kafka" {
		t.Errorf("Stopped = %v, want [kafka]", result.Stopped)
	}
}

func TestEngine_PartialFailure_ReconcileContinuesForOtherServices(t *testing.T) {
	store := openEngineStore(t)
	provider := newFakeProvider()
	provider.failNext = true

	_ = store.UpsertService(&state.Service{
		ID: "svc-bad", Name: "svc-bad", Project: "ums",
		DesiredState: state.StateRunning, ActualState: state.StateStopped,
		Provider: state.ProviderDocker, Config: "{}",
	})
	_ = store.UpsertService(&state.Service{
		ID: "svc-good", Name: "svc-good", Project: "ums",
		DesiredState: state.StateRunning, ActualState: state.StateStopped,
		Provider: state.ProviderDocker, Config: "{}",
	})

	result := runOneCycle(t, store, provider)

	if !result.HasErrors() {
		t.Error("expected errors for svc-bad, got none")
	}
	startedMap := make(map[string]bool)
	for _, id := range result.Started {
		startedMap[id] = true
	}
	if !startedMap["svc-good"] {
		t.Errorf("svc-good was not started; Started = %v", result.Started)
	}
}

func TestEngine_ReconcileResultSummary_ContainsRequiredFields(t *testing.T) {
	store := openEngineStore(t)
	_ = store.UpsertService(&state.Service{
		ID: "svc-x", Name: "svc-x", Project: "p",
		DesiredState: state.StateRunning, ActualState: state.StateStopped,
		Provider: state.ProviderDocker, Config: "{}",
	})

	result := runOneCycle(t, store, newFakeProvider())
	summary := result.Summary()

	for _, want := range []string{"cycle=", "started=", "stopped=", "errors=", "duration="} {
		if !strContains(summary, want) {
			t.Errorf("Summary() missing %q — got: %s", want, summary)
		}
	}
}

func strContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
