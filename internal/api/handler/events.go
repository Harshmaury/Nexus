// @nexus-project: nexus
// @nexus-path: internal/api/handler/events.go
// EventsHandler handles all /events routes.
// Handlers are thin adapters — parse → store query → respond.
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

// List handles GET /events
// Returns recent platform events in reverse-chronological order.
// Supports optional query params:
//
//	?limit=N   number of events to return (default 20, max 200)
//	?trace=<id> filter by trace ID
func (h *EventsHandler) List(w http.ResponseWriter, r *http.Request) {
	limit := defaultEventLimit

	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			respondErr(w, http.StatusBadRequest,
				fmt.Errorf("limit must be a positive integer, got %q", raw))
			return
		}
		if n > maxEventLimit {
			n = maxEventLimit // silently cap — don't error on overage
		}
		limit = n
	}

	traceFilter := r.URL.Query().Get("trace")

	if traceFilter != "" {
		events, err := h.store.GetEventsByTrace(traceFilter)
		if err != nil {
			respondErr(w, http.StatusInternalServerError,
				fmt.Errorf("get events by trace %q: %w", traceFilter, err))
			return
		}
		respondOK(w, events)
		return
	}

	events, err := h.store.GetRecentEvents(limit)
	if err != nil {
		respondErr(w, http.StatusInternalServerError,
			fmt.Errorf("get events: %w", err))
		return
	}
	respondOK(w, events)
}
