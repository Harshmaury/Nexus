// @nexus-project: nexus
// @nexus-path: internal/state/events.go
// Package state — EventWriter provides a typed, ergonomic API for appending events.
// Instead of calling AppendEvent with raw strings everywhere,
// all components use EventWriter methods that enforce correct types.
//
// Phase 15: component and outcome fields added to every event.
//   component — platform domain that emitted the event (nexus|atlas|forge|drop|system).
//   outcome   — result of the action (success|failure|deferred|"" for informational).
//
// These fields make GET /events a rich observation surface for future
// observer services (Metrics, Navigator) without needing a separate bus.
package state

import (
	"encoding/json"
	"fmt"
	"time"
)

// ── OUTCOME CONSTANTS ────────────────────────────────────────────────────────

// Outcome values for event enrichment (Phase 15).
const (
	OutcomeSuccess  = "success"
	OutcomeFailure  = "failure"
	OutcomeDeferred = "deferred"
	OutcomeInfo     = "" // informational — no actionable result
)

// ── COMPONENT CONSTANTS ──────────────────────────────────────────────────────

// Component identifies which platform domain emitted the event (Phase 15).
const (
	ComponentNexus  = "nexus"
	ComponentDrop   = "drop"
	ComponentSystem = "system"
)

// ── EVENT WRITER ─────────────────────────────────────────────────────────────

// EventWriter wraps Store and provides typed event-writing methods.
// Every component that emits events holds an EventWriter, not a raw Store.
// This ensures source, component, and trace IDs are always set correctly.
type EventWriter struct {
	store     Storer
	source    EventSource
	component string
}

// NewEventWriter creates an EventWriter bound to a specific source component.
func NewEventWriter(store Storer, source EventSource, component string) *EventWriter {
	return &EventWriter{store: store, source: source, component: component}
}

// ── TYPED WRITE METHODS ──────────────────────────────────────────────────────

// ServiceStarted records that a service was started.
func (w *EventWriter) ServiceStarted(serviceID string, traceID string) error {
	return w.write(serviceID, EventServiceStarted, traceID, OutcomeSuccess, map[string]string{
		"service_id": serviceID,
	})
}

// ServiceStopped records that a service was stopped.
func (w *EventWriter) ServiceStopped(serviceID string, traceID string) error {
	return w.write(serviceID, EventServiceStopped, traceID, OutcomeSuccess, map[string]string{
		"service_id": serviceID,
	})
}

// ServiceCrashed records that a service crashed with an exit code and message.
func (w *EventWriter) ServiceCrashed(serviceID string, traceID string, exitCode int, message string) error {
	return w.write(serviceID, EventServiceCrashed, traceID, OutcomeFailure, map[string]any{
		"service_id": serviceID,
		"exit_code":  exitCode,
		"message":    message,
	})
}

// ServiceHealed records that a service recovered after a crash.
func (w *EventWriter) ServiceHealed(serviceID string, traceID string) error {
	return w.write(serviceID, EventServiceHealed, traceID, OutcomeSuccess, map[string]string{
		"service_id": serviceID,
	})
}

// StateChanged records a desired or actual state transition.
func (w *EventWriter) StateChanged(serviceID string, traceID string, from string, to string) error {
	return w.write(serviceID, EventStateChanged, traceID, OutcomeInfo, map[string]string{
		"service_id": serviceID,
		"from":       from,
		"to":         to,
	})
}

// ServiceDeferred records that a service start was deferred (dependency not ready).
func (w *EventWriter) ServiceDeferred(serviceID string, traceID string, waitingOn string) error {
	return w.write(serviceID, EventStateChanged, traceID, OutcomeDeferred, map[string]string{
		"service_id": serviceID,
		"waiting_on": waitingOn,
	})
}

// SystemAlert records a platform-level alert (not service-specific).
func (w *EventWriter) SystemAlert(severity string, message string, context map[string]string) error {
	payload := map[string]any{
		"severity": severity,
		"message":  message,
		"context":  context,
	}
	return w.write("system", EventSystemAlert, newTraceID("alert"), OutcomeInfo, payload)
}

// FileDropped records that Nexus Drop detected a new file.
func (w *EventWriter) FileDropped(originalPath string, fileName string, sizeBytes int64) error {
	return w.write("drop", EventFileDropped, newTraceID("drop"), OutcomeInfo, map[string]any{
		"original_path": originalPath,
		"file_name":     fileName,
		"size_bytes":    sizeBytes,
		"detected_at":   time.Now().UTC().Format(time.RFC3339),
	})
}

// FileRouted records that Nexus Drop successfully routed a file to a project.
func (w *EventWriter) FileRouted(traceID string, originalName string, renamedTo string, project string, destination string, method string, confidence float64) error {
	return w.write("drop", EventFileRouted, traceID, OutcomeSuccess, map[string]any{
		"original_name": originalName,
		"renamed_to":    renamedTo,
		"project":       project,
		"destination":   destination,
		"method":        method,
		"confidence":    confidence,
	})
}

// ── INTERNAL ─────────────────────────────────────────────────────────────────

// write serialises the payload to JSON and delegates to the store.
func (w *EventWriter) write(serviceID string, eventType EventType, traceID string, outcome string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	return w.store.AppendEvent(serviceID, eventType, w.source, traceID, w.component, outcome, string(data))
}

// newTraceID generates a trace ID for events that start a new operation chain.
func newTraceID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
