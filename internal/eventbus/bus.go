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
//
// ADR-002 addition (workspace observation):
//   Nexus owns filesystem observation for the entire platform.
//   The watcher publishes workspace change events through this bus so that
//   Atlas, Forge, and any future service can subscribe without running their
//   own watchers.
//
//   Topic constants are declared here — the single source of truth.
//   ALL consumers (Atlas, Forge, diagnostics) MUST import these constants.
//   NO consumer may redefine topic strings locally.
//
//   Publishers:  internal/watcher/watcher.go (workspace topics)
//   Consumers:   Atlas (index updates), Forge Phase 3 (automation triggers)
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
	// ── Service lifecycle ────────────────────────────────────────────────────
	// Published by: daemon/engine.go, controllers/health.go
	// Consumed by:  result logger, recovery controller
	TopicServiceStarted Topic = "service.started"
	TopicServiceStopped Topic = "service.stopped"
	TopicServiceCrashed Topic = "service.crashed"
	TopicServiceHealed  Topic = "service.healed"
	TopicStateChanged   Topic = "service.state_changed"
	TopicHealthCheck    Topic = "service.health_check"
	TopicRecoveryNeeded Topic = "service.recovery_needed"

	// ── System ───────────────────────────────────────────────────────────────
	// Published by: any component
	// Consumed by:  result logger, diagnostics
	TopicSystemAlert Topic = "system.alert"

	// ── Drop Intelligence ────────────────────────────────────────────────────
	// Published by: watcher (file_detected), router (routed, quarantined, approval)
	// Consumed by:  intelligence pipeline, daemon server (approval map)
	TopicFileDropped     Topic = "drop.file_detected"
	TopicFileRouted      Topic = "drop.file_routed"
	TopicFileQuarantined Topic = "drop.file_quarantined"

	// TopicDropPendingApproval is published when the router cannot auto-route a
	// file (confidence 0.40–0.79) and needs CLI confirmation.
	// The CLI subscribes via the socket and presents an interactive prompt.
	TopicDropPendingApproval Topic = "drop.pending_approval"

	// ── Workspace (ADR-002) ──────────────────────────────────────────────────
	// Published by: internal/watcher/watcher.go (workspace observation)
	// Consumed by:  Atlas (index updates), Forge Phase 3 (automation triggers)
	//
	// RULE: import these constants — never redefine topic strings locally.
	// All consumers must: import "github.com/Harshmaury/Nexus/internal/eventbus"

	// TopicWorkspaceFileCreated is published when a new file appears in the
	// watched workspace directories. Payload: WorkspaceFilePayload.
	TopicWorkspaceFileCreated Topic = "workspace.file.created"

	// TopicWorkspaceFileModified is published when an existing workspace file
	// is written to. Payload: WorkspaceFilePayload.
	TopicWorkspaceFileModified Topic = "workspace.file.modified"

	// TopicWorkspaceFileDeleted is published when a workspace file is removed.
	// Payload: WorkspaceFilePayload.
	TopicWorkspaceFileDeleted Topic = "workspace.file.deleted"

	// TopicWorkspaceUpdated is published after a batch of file events settles
	// (debounce window). Signals a logical workspace change rather than a
	// single file event. Payload: WorkspaceUpdatedPayload.
	TopicWorkspaceUpdated Topic = "workspace.updated"

	// TopicWorkspaceProjectDetected is published when the watcher detects a
	// directory that looks like a new project (contains .nexus.yaml, go.mod,
	// package.json, etc.). Payload: WorkspaceProjectPayload.
	TopicWorkspaceProjectDetected Topic = "workspace.project.detected"
)

// ── WORKSPACE PAYLOADS (ADR-002) ─────────────────────────────────────────────

// WorkspaceFilePayload is published on TopicWorkspaceFileCreated,
// TopicWorkspaceFileModified, and TopicWorkspaceFileDeleted.
type WorkspaceFilePayload struct {
	Path      string    // absolute path to the file
	Name      string    // base filename
	Extension string    // file extension including dot, e.g. ".go"
	SizeBytes int64     // 0 for deleted files
	EventAt   time.Time // when the filesystem event was observed
}

// WorkspaceUpdatedPayload is published on TopicWorkspaceUpdated.
// Summarises a settled batch of changes rather than individual file events.
type WorkspaceUpdatedPayload struct {
	WatchDir  string    // the root directory that changed
	EventAt   time.Time // when the batch settled
}

// WorkspaceProjectPayload is published on TopicWorkspaceProjectDetected.
type WorkspaceProjectPayload struct {
	Path        string    // absolute path to the project root
	Name        string    // directory name
	DetectedBy  string    // which manifest triggered detection: "nexus.yaml"|"go.mod"|"package.json"|...
	DetectedAt  time.Time
}

// ── EXISTING PAYLOADS ────────────────────────────────────────────────────────

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
	wg            sync.WaitGroup
	eventCounter  atomic.Uint64
	subCounter    atomic.Uint64
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
func (b *Bus) Subscribe(topic Topic, handler Handler) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.generateSubscriptionID(topic)
	sub := &subscription{id: id, topic: topic, handler: handler}
	b.subscriptions[topic] = append(b.subscriptions[topic], sub)
	return id
}

// SubscribeAll registers a handler that receives every event on every topic.
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
func (b *Bus) PublishAsync(topic Topic, serviceID string, payload any) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.Publish(topic, serviceID, payload)
	}()
}

// ── EXISTING TYPED PAYLOADS ───────────────────────────────────────────────────

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
	Severity string
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
type DropApprovalPayload struct {
	FilePath    string
	ProjectID   string
	Destination string
	Confidence  float64
	Method      string
}

// ── WAIT / INTROSPECTION ──────────────────────────────────────────────────────

// Wait blocks until all goroutines spawned by PublishAsync have completed.
func (b *Bus) Wait() {
	b.wg.Wait()
}

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
	return fmt.Sprintf("evt-%d", b.eventCounter.Add(1))
}

func (b *Bus) generateSubscriptionID(topic Topic) string {
	return fmt.Sprintf("sub-%s-%d", topic, b.subCounter.Add(1))
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
