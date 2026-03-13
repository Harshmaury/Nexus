// @nexus-project: nexus
// @nexus-path: internal/eventbus/bus.go
// Package eventbus provides an in-process pub/sub event bus.
// Every component in Nexus communicates exclusively through this bus.
// No component imports another directly — only the bus is shared.
package eventbus

import (
	"fmt"
	"sync"
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
)

// Event carries data between components.
// Publishers set Topic and Payload. Bus fills ID and PublishedAt.
type Event struct {
	ID          string
	Topic       Topic
	ServiceID   string // empty for system-level events
	Payload     any    // typed payload — cast after receiving
	PublishedAt time.Time
}

// Handler is a function that processes an event from the bus.
// Returning an error logs it but does not stop other handlers.
type Handler func(event Event) error

// subscription links a handler to a topic with a unique ID.
type subscription struct {
	id      string
	topic   Topic
	handler Handler
}

// ── BUS ──────────────────────────────────────────────────────────────────────

// Bus is the central event router for the Nexus daemon.
// All components hold a reference to the same Bus instance.
type Bus struct {
	mu            sync.RWMutex
	subscriptions map[Topic][]*subscription
	errorHandler  func(topic Topic, handlerID string, err error)
	eventCounter  uint64
}

// New creates a new Bus with a default error handler that prints to stderr.
func New() *Bus {
	return &Bus{
		subscriptions: make(map[Topic][]*subscription),
		errorHandler:  defaultErrorHandler,
	}
}

// NewWithErrorHandler creates a Bus with a custom error handler.
// Use this in production to route handler errors to your logger.
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

	id := generateSubscriptionID(topic)
	sub := &subscription{
		id:      id,
		topic:   topic,
		handler: handler,
	}
	b.subscriptions[topic] = append(b.subscriptions[topic], sub)
	return id
}

// SubscribeAll registers a handler that receives events from every topic.
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

// Publish sends an event to all subscribers of its topic.
// Also delivers to wildcard ("*") subscribers.
// All handlers are called synchronously in the calling goroutine.
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
// Use for fire-and-forget notifications (e.g. Windows toast, terminal notify).
func (b *Bus) PublishAsync(topic Topic, serviceID string, payload any) {
	go b.Publish(topic, serviceID, payload)
}

// ── TYPED PAYLOADS ───────────────────────────────────────────────────────────
// These structs give each topic a concrete, type-safe payload.
// Cast event.Payload to the correct type after receiving.

// StateChangedPayload is published on TopicStateChanged.
type StateChangedPayload struct {
	ServiceID string
	From      string
	To        string
}

// HealthCheckPayload is published on TopicHealthCheck.
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

// ── INTROSPECTION ────────────────────────────────────────────────────────────

// SubscriberCount returns how many handlers are registered for a topic.
// Useful for health checks and debugging.
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
	b.mu.Lock()
	b.eventCounter++
	n := b.eventCounter
	b.mu.Unlock()
	return fmt.Sprintf("evt-%d-%d", time.Now().UnixNano(), n)
}

func generateSubscriptionID(topic Topic) string {
	return fmt.Sprintf("sub-%s-%d", topic, time.Now().UnixNano())
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
