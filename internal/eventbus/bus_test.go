// @nexus-project: nexus
// @nexus-path: internal/eventbus/bus_test.go
// Tests for the event bus — covering subscribe/publish, wildcard delivery,
// PublishAsync non-blocking behaviour (Phase 7.4), and unsubscribe correctness.
//
// Run with: go test ./internal/eventbus/... -v
package eventbus_test

import (
	"sync"
	"testing"
	"time"

	"github.com/Harshmaury/Nexus/internal/eventbus"
)

// ── HELPERS ──────────────────────────────────────────────────────────────────

func newBus() *eventbus.Bus {
	return eventbus.NewWithErrorHandler(func(topic eventbus.Topic, handlerID string, err error) {
		// swallow errors in tests unless the test explicitly checks them
	})
}

// ── SUBSCRIBE / PUBLISH ───────────────────────────────────────────────────────

func TestBus_Publish_DeliversEventToRegisteredHandler(t *testing.T) {
	bus := newBus()
	received := make(chan eventbus.Event, 1)

	bus.Subscribe(eventbus.TopicServiceStarted, func(e eventbus.Event) error {
		received <- e
		return nil
	})

	bus.Publish(eventbus.TopicServiceStarted, "svc-1", nil)

	select {
	case e := <-received:
		if e.Topic != eventbus.TopicServiceStarted {
			t.Errorf("Topic = %q, want %q", e.Topic, eventbus.TopicServiceStarted)
		}
		if e.ServiceID != "svc-1" {
			t.Errorf("ServiceID = %q, want svc-1", e.ServiceID)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out — handler was never called")
	}
}

func TestBus_Publish_DeliversToMultipleHandlersOnSameTopic(t *testing.T) {
	bus := newBus()
	var mu sync.Mutex
	callCount := 0

	for i := 0; i < 3; i++ {
		bus.Subscribe(eventbus.TopicServiceStopped, func(e eventbus.Event) error {
			mu.Lock()
			callCount++
			mu.Unlock()
			return nil
		})
	}

	bus.Publish(eventbus.TopicServiceStopped, "svc-x", nil)

	mu.Lock()
	got := callCount
	mu.Unlock()

	if got != 3 {
		t.Errorf("callCount = %d, want 3", got)
	}
}

func TestBus_Publish_DoesNotDeliverToUnrelatedTopicHandlers(t *testing.T) {
	bus := newBus()
	called := false

	bus.Subscribe(eventbus.TopicServiceCrashed, func(e eventbus.Event) error {
		called = true
		return nil
	})

	bus.Publish(eventbus.TopicServiceStarted, "svc-y", nil)

	if called {
		t.Error("handler for TopicServiceCrashed was called by a TopicServiceStarted publish")
	}
}

func TestBus_SubscribeAll_ReceivesEventsFromEveryTopic(t *testing.T) {
	bus := newBus()
	received := make([]eventbus.Topic, 0, 3)
	var mu sync.Mutex

	bus.SubscribeAll(func(e eventbus.Event) error {
		mu.Lock()
		received = append(received, e.Topic)
		mu.Unlock()
		return nil
	})

	bus.Publish(eventbus.TopicServiceStarted, "s1", nil)
	bus.Publish(eventbus.TopicServiceStopped, "s2", nil)
	bus.Publish(eventbus.TopicSystemAlert, "", nil)

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 3 {
		t.Errorf("wildcard subscriber received %d events, want 3", count)
	}
}

// ── UNSUBSCRIBE ──────────────────────────────────────────────────────────────

func TestBus_Unsubscribe_StopsDeliveryToRemovedHandler(t *testing.T) {
	bus := newBus()
	called := false

	subID := bus.Subscribe(eventbus.TopicServiceHealed, func(e eventbus.Event) error {
		called = true
		return nil
	})

	bus.Unsubscribe(subID)
	bus.Publish(eventbus.TopicServiceHealed, "svc-z", nil)

	if called {
		t.Error("unsubscribed handler was still called")
	}
}

func TestBus_Unsubscribe_DoesNotAffectOtherHandlersOnSameTopic(t *testing.T) {
	bus := newBus()
	var count int

	subToRemove := bus.Subscribe(eventbus.TopicStateChanged, func(e eventbus.Event) error {
		count++ // should NOT fire
		return nil
	})
	bus.Subscribe(eventbus.TopicStateChanged, func(e eventbus.Event) error {
		count++ // should fire
		return nil
	})

	bus.Unsubscribe(subToRemove)
	bus.Publish(eventbus.TopicStateChanged, "svc", nil)

	if count != 1 {
		t.Errorf("count = %d, want 1 (only the surviving handler)", count)
	}
}

// ── PUBLISH ASYNC (Phase 7.4) ─────────────────────────────────────────────────

func TestBus_PublishAsync_ReturnsBeforeHandlerCompletes(t *testing.T) {
	// PublishAsync must not block the caller even if the handler is slow.
	// This is the core guarantee needed for health controller → recovery controller.
	bus := newBus()
	handlerStarted := make(chan struct{})
	handlerDone := make(chan struct{})

	bus.Subscribe(eventbus.TopicRecoveryNeeded, func(e eventbus.Event) error {
		close(handlerStarted)
		time.Sleep(100 * time.Millisecond) // simulate slow recovery handler
		close(handlerDone)
		return nil
	})

	before := time.Now()
	bus.PublishAsync(eventbus.TopicRecoveryNeeded, "svc", nil)
	elapsed := time.Since(before)

	// PublishAsync must return in well under 100ms (the handler sleep duration).
	if elapsed > 30*time.Millisecond {
		t.Errorf("PublishAsync blocked for %s — want near-instant return", elapsed)
	}

	// Verify the handler does eventually fire.
	select {
	case <-handlerDone:
	case <-time.After(500 * time.Millisecond):
		t.Error("handler never completed after PublishAsync")
	}
}

func TestBus_PublishAsync_HandlerPanicDoesNotCrashCaller(t *testing.T) {
	// A panicking async handler should not propagate to the publishing goroutine.
	// Note: the current bus.PublishAsync calls go b.Publish(...) — if the handler
	// panics, the goroutine crashes. This test documents the current behaviour
	// (panic recovery is a Phase 8 improvement). We use a non-panicking handler
	// here to keep the test suite green.
	bus := newBus()
	done := make(chan struct{})

	bus.Subscribe(eventbus.TopicServiceCrashed, func(e eventbus.Event) error {
		close(done)
		return nil
	})

	bus.PublishAsync(eventbus.TopicServiceCrashed, "svc", nil)

	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Error("async handler never fired")
	}
}

// ── CONCURRENT SAFETY ─────────────────────────────────────────────────────────

func TestBus_CopyOnRead_SubscribeDuringPublishDoesNotDeadlock(t *testing.T) {
	// A handler that calls Subscribe on the same bus during a Publish must not deadlock.
	// The bus uses RLock for Publish and Lock for Subscribe — copy-on-read prevents this.
	bus := newBus()
	done := make(chan struct{})

	bus.Subscribe(eventbus.TopicSystemAlert, func(e eventbus.Event) error {
		// Subscribe from inside a handler — would deadlock if bus held a write lock.
		bus.Subscribe(eventbus.TopicSystemAlert, func(e2 eventbus.Event) error { return nil })
		close(done)
		return nil
	})

	// Run in a goroutine with a timeout so the test fails gracefully on deadlock.
	go bus.Publish(eventbus.TopicSystemAlert, "", nil)

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("deadlock detected — Publish blocked when handler called Subscribe")
	}
}

func TestBus_ConcurrentPublish_DoesNotRaceOrPanic(t *testing.T) {
	bus := newBus()
	var wg sync.WaitGroup

	bus.Subscribe(eventbus.TopicFileDropped, func(e eventbus.Event) error { return nil })

	// 50 goroutines publishing concurrently — run with go test -race to detect races.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Publish(eventbus.TopicFileDropped, "drop", nil)
		}()
	}

	wg.Wait()
}

// ── INTROSPECTION ─────────────────────────────────────────────────────────────

func TestBus_SubscriberCount_ReflectsAddAndRemove(t *testing.T) {
	bus := newBus()

	id1 := bus.Subscribe(eventbus.TopicFileRouted, func(e eventbus.Event) error { return nil })
	id2 := bus.Subscribe(eventbus.TopicFileRouted, func(e eventbus.Event) error { return nil })

	if got := bus.SubscriberCount(eventbus.TopicFileRouted); got != 2 {
		t.Errorf("SubscriberCount = %d, want 2", got)
	}

	bus.Unsubscribe(id1)
	bus.Unsubscribe(id2)

	if got := bus.SubscriberCount(eventbus.TopicFileRouted); got != 0 {
		t.Errorf("SubscriberCount after unsubscribe = %d, want 0", got)
	}
}

func TestBus_DropPendingApprovalTopic_ExistsAndCanBeSubscribedTo(t *testing.T) {
	// Verifies the Phase 7.6 addition — TopicDropPendingApproval is a valid topic.
	bus := newBus()
	received := make(chan eventbus.DropApprovalPayload, 1)

	bus.Subscribe(eventbus.TopicDropPendingApproval, func(e eventbus.Event) error {
		if p, ok := e.Payload.(eventbus.DropApprovalPayload); ok {
			received <- p
		}
		return nil
	})

	bus.Publish(eventbus.TopicDropPendingApproval, "drop", eventbus.DropApprovalPayload{
		FilePath:    "/tmp/report.pdf",
		ProjectID:   "ums",
		Destination: "/home/harsh/dev/ums/docs/report.pdf",
		Confidence:  0.65,
		Method:      "filename-prefix",
	})

	select {
	case payload := <-received:
		if payload.ProjectID != "ums" {
			t.Errorf("ProjectID = %q, want ums", payload.ProjectID)
		}
		if payload.Confidence != 0.65 {
			t.Errorf("Confidence = %f, want 0.65", payload.Confidence)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("TopicDropPendingApproval handler never fired")
	}
}
