// @nexus-project: nexus
// @nexus-path: internal/api/handler/events.go
// EventsHandler handles all /events routes.
// Handlers are thin adapters — parse → store query → respond.
//
// Phase 15: added ?since=<id> query param for efficient incremental polling.
// Atlas subscriber uses this instead of fetching all recent events on every tick.
//
// Supported query params:
//   ?limit=N       number of events (default 20, max 200)
//   ?trace=<id>    filter by trace ID — returns full trace in ASC order
//   ?since=<id>    return events with ID > N in ASC order (for polling)
//
// since and trace are mutually exclusive. since takes priority if both present.
package handler

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/Harshmaury/Nexus/internal/state"
)

const (
	defaultEventLimit = 20
	maxEventLimit     = 200
)

// EventsHandler handles all /events routes.
type EventsHandler struct {
	store state.Storer
}

// NewEventsHandler creates an EventsHandler.
func NewEventsHandler(store state.Storer) *EventsHandler {
	return &EventsHandler{store: store}
}

// List handles GET /events.
// Returns platform events. Behaviour depends on query params:
//
//	?since=<id>   events with ID > N, ascending (incremental polling)
//	?trace=<id>   all events sharing a trace ID, ascending
//	(default)     most recent N events, descending
func (h *EventsHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err)
		return
	}

	if sinceRaw := r.URL.Query().Get("since"); sinceRaw != "" {
		h.listSince(w, r, sinceRaw, limit)
		return
	}

	if traceID := r.URL.Query().Get("trace"); traceID != "" {
		h.listByTrace(w, traceID)
		return
	}

	h.listRecent(w, limit)
}

// listSince returns events with ID greater than the given value.
// Used by Atlas subscriber for efficient incremental polling (Phase 15).
func (h *EventsHandler) listSince(w http.ResponseWriter, r *http.Request, sinceRaw string, limit int) {
	sinceID, err := strconv.ParseInt(sinceRaw, 10, 64)
	if err != nil || sinceID < 0 {
		respondErr(w, http.StatusBadRequest,
			fmt.Errorf("since must be a non-negative integer, got %q", sinceRaw))
		return
	}

	events, err := h.store.GetEventsSince(sinceID, limit)
	if err != nil {
		respondErr(w, http.StatusInternalServerError,
			fmt.Errorf("get events since %d: %w", sinceID, err))
		return
	}
	respondOK(w, events)
}

// listByTrace returns all events sharing a trace ID.
func (h *EventsHandler) listByTrace(w http.ResponseWriter, traceID string) {
	events, err := h.store.GetEventsByTrace(traceID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError,
			fmt.Errorf("get events by trace %q: %w", traceID, err))
		return
	}
	respondOK(w, events)
}

// listRecent returns the N most recent events in descending order.
func (h *EventsHandler) listRecent(w http.ResponseWriter, limit int) {
	events, err := h.store.GetRecentEvents(limit)
	if err != nil {
		respondErr(w, http.StatusInternalServerError,
			fmt.Errorf("get events: %w", err))
		return
	}
	respondOK(w, events)
}

// parseLimit extracts and validates the ?limit query param.
func parseLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultEventLimit, nil
	}

	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("limit must be a positive integer, got %q", raw)
	}

	if n > maxEventLimit {
		n = maxEventLimit
	}
	return n, nil
}
