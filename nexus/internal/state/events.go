// @nexus-project: nexus
// @nexus-path: internal/state/events.go
// Package state — EventWriter provides a typed, ergonomic API for appending events.
// Instead of calling AppendEvent with raw strings everywhere,
// all components use EventWriter methods that enforce correct types.
package state

import (
	"encoding/json"
	"fmt"
	"time"
)

// ── EVENT WRITER ─────────────────────────────────────────────────────────────

// EventWriter wraps Store and provides typed event-writing methods.
// Every component that emits events holds an EventWriter, not a raw Store.
// This ensures source and trace IDs are always set correctly.
type EventWriter struct {
	store  *Store
	source EventSource
}

// NewEventWriter creates an EventWriter bound to a specific source component.
func NewEventWriter(store *Store, source EventSource) *EventWriter {
	return &EventWriter{store: store, source: source}
}

// ── TYPED WRITE METHODS ──────────────────────────────────────────────────────

// ServiceStarted records that a service was started.
func (w *EventWriter) ServiceStarted(serviceID string, traceID string) error {
	return w.write(serviceID, EventServiceStarted, traceID, map[string]string{
		"service_id": serviceID,
	})
}

// ServiceStopped records that a service was stopped.
func (w *EventWriter) ServiceStopped(serviceID string, traceID string) error {
	return w.write(serviceID, EventServiceStopped, traceID, map[string]string{
		"service_id": serviceID,
	})
}

// ServiceCrashed records that a service crashed with an exit code and message.
func (w *EventWriter) ServiceCrashed(serviceID string, traceID string, exitCode int, message string) error {
	return w.write(serviceID, EventServiceCrashed, traceID, map[string]any{
		"service_id": serviceID,
		"exit_code":  exitCode,
		"message":    message,
	})
}

// ServiceHealed records that a service recovered after a crash.
func (w *EventWriter) ServiceHealed(serviceID string, traceID string) error {
	return w.write(serviceID, EventServiceHealed, traceID, map[string]string{
		"service_id": serviceID,
	})
}

// StateChanged records a desired or actual state transition.
func (w *EventWriter) StateChanged(serviceID string, traceID string, from string, to string) error {
	return w.write(serviceID, EventStateChanged, traceID, map[string]string{
		"service_id": serviceID,
		"from":       from,
		"to":         to,
	})
}

// SystemAlert records a platform-level alert (not service-specific).
func (w *EventWriter) SystemAlert(severity string, message string, context map[string]string) error {
	payload := map[string]any{
		"severity": severity,
		"message":  message,
		"context":  context,
	}
	return w.write("system", EventSystemAlert, newTraceID("alert"), payload)
}

// FileDropped records that Nexus Drop detected a new file.
func (w *EventWriter) FileDropped(originalPath string, fileName string, sizeBytes int64) error {
	return w.write("drop", EventFileDropped, newTraceID("drop"), map[string]any{
		"original_path": originalPath,
		"file_name":     fileName,
		"size_bytes":    sizeBytes,
		"detected_at":   time.Now().UTC().Format(time.RFC3339),
	})
}

// FileRouted records that Nexus Drop successfully routed a file to a project.
func (w *EventWriter) FileRouted(traceID string, originalName string, renamedTo string, project string, destination string, method string, confidence float64) error {
	return w.write("drop", EventFileRouted, traceID, map[string]any{
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
func (w *EventWriter) write(serviceID string, eventType EventType, traceID string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	return w.store.AppendEvent(serviceID, eventType, w.source, traceID, string(data))
}

// newTraceID generates a trace ID for events that start a new operation chain.
func newTraceID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
