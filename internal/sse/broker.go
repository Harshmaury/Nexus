// @nexus-project: nexus
// @nexus-path: internal/sse/broker.go
// Package sse implements a fan-out broker for Server-Sent Events (ADR-015).
//
// The Broker maintains a registry of connected SSE clients. When an event
// is published, it is broadcast to all clients concurrently. Slow clients
// are dropped after sendTimeout to prevent blocking the event bus.
//
// Thread safety: all subscriber operations are protected by a mutex.
// Publish is non-blocking from the caller's perspective.
package sse

import (
	"encoding/json"
	"sync"
	"time"
)

const (
	sendTimeout    = 5 * time.Second
	subscriberBuf  = 64 // per-client channel buffer
)

// Event is a platform event broadcast over SSE.
type Event struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"`
	ServiceID string    `json:"service_id"`
	Source    string    `json:"source"`
	Component string    `json:"component"`
	Outcome   string    `json:"outcome"`
	TraceID   string    `json:"trace_id"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

// Subscriber is a connected SSE client channel.
type Subscriber chan []byte

// Broker fans out events to all connected SSE subscribers.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[Subscriber]struct{}
}

// NewBroker creates an empty Broker.
func NewBroker() *Broker {
	return &Broker{
		subscribers: make(map[Subscriber]struct{}),
	}
}

// Subscribe registers a new SSE client and returns its channel.
// The caller must call Unsubscribe when the client disconnects.
func (b *Broker) Subscribe() Subscriber {
	ch := make(Subscriber, subscriberBuf)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a client and closes its channel.
func (b *Broker) Unsubscribe(ch Subscriber) {
	b.mu.Lock()
	delete(b.subscribers, ch)
	b.mu.Unlock()
	close(ch)
}

// Publish broadcasts an event to all connected subscribers.
// Each subscriber gets a non-blocking send with sendTimeout.
// Slow subscribers that can't receive within the timeout are dropped.
func (b *Broker) Publish(evt *Event) {
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	msg := append([]byte("data: "), data...)
	msg = append(msg, '\n', '\n')

	b.mu.RLock()
	subs := make([]Subscriber, 0, len(b.subscribers))
	for ch := range b.subscribers {
		subs = append(subs, ch)
	}
	b.mu.RUnlock()

	var slow []Subscriber
	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
			// Channel full — try with timeout.
			timer := time.NewTimer(sendTimeout)
			select {
			case ch <- msg:
				timer.Stop()
			case <-timer.C:
				slow = append(slow, ch)
			}
		}
	}

	// Drop slow subscribers outside the lock.
	for _, ch := range slow {
		b.Unsubscribe(ch)
	}
}

// SubscriberCount returns the number of connected SSE clients.
func (b *Broker) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

// PublishRaw builds an Event from raw fields and publishes it.
// Satisfies the state.SSEPublisher interface so EventWriter can notify
// the broker without importing the sse package directly.
func (b *Broker) PublishRaw(eventType, serviceID, source, component, outcome, traceID, payload string) {
	b.Publish(&Event{
		Type:      eventType,
		ServiceID: serviceID,
		Source:    source,
		Component: component,
		Outcome:   outcome,
		TraceID:   traceID,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
}
