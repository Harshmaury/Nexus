// @nexus-project: nexus
// @nexus-path: internal/eventbus/bus.go
// Package eventbus provides an in-process pub/sub event bus.
// Every component in Nexus communicates exclusively through this bus.
// No component imports another directly — only the bus is shared.
//
// Phase 7.6 addition:
//   - TopicDropPendingApproval — published by the Router when a file's confidence
//     falls in the prompt range (0.40–0.79). The CLI (engx) watches the socket
//     for this event and presents the interactive approval prompt.
//   - DropApprovalPayload — carries all routing details needed for the CLI prompt.
package eventbus

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ── TYPES ────────────────────────────────────────────────────────────────────

// Topic is a named channel on the bus.
type Topic string

const (
	TopicServiceStarted  Topic = "service.started"
	TopicServiceStopped  Topic = "service.stopped"
	TopicServiceCrashed  Topic = "service.crashed"
	TopicServiceHealed   Topic = "service.healed"
	TopicStateChanged    Topic = "service.state_changed"
	TopicHealthCheck     Topic = "service.health_check"
	TopicRecoveryNeeded  Topic = "service.recovery_needed"
	TopicSystemAlert     Topic = "system.alert"
	TopicFileDropped     Topic = "drop.file_detected"
	TopicFileRouted      Topic = "drop.file_routed"
	TopicFileQuarantined Topic = "drop.file_quarantined"

	// TopicDropPendingApproval is published when the router cannot auto-route a
	// file (confidence 0.40–0.79) and needs CLI confirmation.
	// The CLI subscribes via the socket and presents an interactive prompt.
	TopicDropPendingApproval Topic = "drop.pending_approval"
)

// Event carries data between components.
type Event struct {
	ID          string
	Topic       Topic
	ServiceID   string
	Payload     any
	PublishedAt time.Time
}

// Handler is a function that processes an event from the bus.
type Handler func(event Event) error

type subscription struct {
	id      string
	topic   Topic
	handler Handler
}

// ── BUS ──────────────────────────────────────────────────────────────────────

// Bus is the central event router for the Nexus daemon.
type Bus struct {
	mu            sync.RWMutex
	subscriptions map[Topic][]*subscription
	errorHandler  func(topic Topic, handlerID string, err error)
	wg            sync.WaitGroup // tracks in-flight PublishAsync goroutines
	eventCounter  atomic.Uint64  // monotonic counter for event IDs
	subCounter    atomic.Uint64  // monotonic counter for subscription IDs
}

// New creates a new Bus with a default error handler that prints to stderr.
func New() *Bus {
	return &Bus{
		subscriptions: make(map[Topic][]*subscription),
		errorHandler:  defaultErrorHandler,
	}
}

// NewWithErrorHandler creates a Bus with a custom error handler.
func NewWithErrorHandler(onError func(topic Topic, handlerID string, err error)) *Bus {
	return &Bus{
		subscriptions: make(map[Topic][]*subscription),
		errorHandler:  onError,
	}
}

// ── SUBSCRIBE ────────────────────────────────────────────────────────────────

// Subscribe registers a handler for a topic.
// Returns a subscription ID that can be used to Unsubscribe later.
func (b *Bus) Subscribe(topic Topic, handler Handler) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.generateSubscriptionID(topic)
	sub := &subscription{id: id, topic: topic, handler: handler}
	b.subscriptions[topic] = append(b.subscriptions[topic], sub)
	return id
}

// SubscribeAll registers a handler that receives every event on every topic.
// Useful for audit logging and debugging.
func (b *Bus) SubscribeAll(handler Handler) string {
	return b.Subscribe("*", handler)
}

// Unsubscribe removes a handler by its subscription ID.
func (b *Bus) Unsubscribe(subscriptionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for topic, subs := range b.subscriptions {
		filtered := subs[:0]
		for _, sub := range subs {
			if sub.id != subscriptionID {
				filtered = append(filtered, sub)
			}
		}
		b.subscriptions[topic] = filtered
	}
}

// ── PUBLISH ──────────────────────────────────────────────────────────────────

// Publish sends an event synchronously to all subscribers of the topic.
// Also delivers to wildcard ("*") subscribers.
// Handlers are called in the caller's goroutine — a slow handler blocks the caller.
// Use PublishAsync for events that trigger long-running handlers (e.g. recovery).
func (b *Bus) Publish(topic Topic, serviceID string, payload any) {
	b.mu.RLock()
	topicSubs := copySlice(b.subscriptions[topic])
	wildcardSubs := copySlice(b.subscriptions["*"])
	b.mu.RUnlock()

	event := Event{
		ID:          b.nextEventID(),
		Topic:       topic,
		ServiceID:   serviceID,
		Payload:     payload,
		PublishedAt: time.Now().UTC(),
	}

	allSubs := append(topicSubs, wildcardSubs...)
	for _, sub := range allSubs {
		if err := sub.handler(event); err != nil {
			b.errorHandler(topic, sub.id, err)
		}
	}
}

// PublishAsync sends an event in a new goroutine so the caller is never blocked.
// Used for TopicServiceCrashed and TopicRecoveryNeeded where the recovery handler
// involves store reads/writes and must not block the health polling loop.
//
// Fix 04: goroutine is tracked in b.wg so Bus.Wait() can block until all
// async handlers complete before the store is closed on shutdown.
func (b *Bus) PublishAsync(topic Topic, serviceID string, payload any) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.Publish(topic, serviceID, payload)
	}()
}

// ── TYPED PAYLOADS ───────────────────────────────────────────────────────────

// StateChangedPayload is published on TopicStateChanged.
type StateChangedPayload struct {
	ServiceID string
	From      string
	To        string
}

// HealthCheckPayload is published on TopicHealthCheck, TopicServiceCrashed, TopicServiceHealed.
type HealthCheckPayload struct {
	ServiceID string
	Status    string
	ExitCode  int
	Message   string
}

// RecoveryPayload is published on TopicRecoveryNeeded.
type RecoveryPayload struct {
	ServiceID  string
	FailCount  int
	LastFailed time.Time
}

// AlertPayload is published on TopicSystemAlert.
type AlertPayload struct {
	Severity string // info | warn | critical
	Message  string
	Context  map[string]string
}

// FileDropPayload is published on TopicFileDropped.
type FileDropPayload struct {
	OriginalPath string
	FileName     string
	SizeBytes    int64
	DetectedAt   time.Time
}

// FileRoutedPayload is published on TopicFileRouted.
type FileRoutedPayload struct {
	OriginalName string
	RenamedTo    string
	Project      string
	Destination  string
	Method       string
	Confidence   float64
}

// DropApprovalPayload is published on TopicDropPendingApproval.
// The CLI receives this via the socket and prompts the user for confirmation.
// Replaces the former blocking bufio.NewReader(os.Stdin) in the router.
type DropApprovalPayload struct {
	FilePath    string  // full path to the original file
	ProjectID   string  // detected project
	Destination string  // where it would be moved on approval
	Confidence  float64 // detection confidence (0.40–0.79)
	Method      string  // detection method used
}

// Wait blocks until all goroutines spawned by PublishAsync have completed.
// Call this during shutdown before closing the state store to ensure
// in-flight recovery handlers finish their DB writes cleanly.
func (b *Bus) Wait() {
	b.wg.Wait()
}

// ── INTROSPECTION ────────────────────────────────────────────────────────────

// SubscriberCount returns how many handlers are registered for a topic.
func (b *Bus) SubscriberCount(topic Topic) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscriptions[topic])
}

// Topics returns all topics that currently have at least one subscriber.
func (b *Bus) Topics() []Topic {
	b.mu.RLock()
	defer b.mu.RUnlock()

	topics := make([]Topic, 0, len(b.subscriptions))
	for topic, subs := range b.subscriptions {
		if len(subs) > 0 {
			topics = append(topics, topic)
		}
	}
	return topics
}

// Reset removes all subscriptions. Used in tests only.
func (b *Bus) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscriptions = make(map[Topic][]*subscription)
}

// ── HELPERS ──────────────────────────────────────────────────────────────────

func (b *Bus) nextEventID() string {
	n := b.eventCounter.Add(1)
	return fmt.Sprintf("evt-%d", n)
}

// generateSubscriptionID returns a guaranteed-unique ID for a subscription.
// Uses an atomic monotonic counter — collision-proof even under concurrent
// Subscribe() calls on modern hardware where UnixNano() can repeat.
func (b *Bus) generateSubscriptionID(topic Topic) string {
	n := b.subCounter.Add(1)
	return fmt.Sprintf("sub-%s-%d", topic, n)
}

func copySlice(src []*subscription) []*subscription {
	if len(src) == 0 {
		return nil
	}
	dst := make([]*subscription, len(src))
	copy(dst, src)
	return dst
}

func defaultErrorHandler(topic Topic, handlerID string, err error) {
	fmt.Printf("[eventbus] handler %s on topic %s returned error: %v\n", handlerID, topic, err)
}
