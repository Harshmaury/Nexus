// @nexus-project: nexus
// @nexus-path: internal/api/handler/stream.go
package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Harshmaury/Nexus/internal/sse"
)

const keepaliveInterval = 15 * time.Second

type StreamHandler struct {
	broker *sse.Broker
}

func NewStreamHandler(broker *sse.Broker) *StreamHandler {
	return &StreamHandler{broker: broker}
}

func (h *StreamHandler) Stream(w http.ResponseWriter, r *http.Request) {
	// Walk the wrapper chain to find a Flusher.
	var flusher http.Flusher
	cur := w
	for {
		if f, ok := cur.(http.Flusher); ok {
			flusher = f
			break
		}
		type unwrapper interface{ Unwrap() http.ResponseWriter }
		uw, ok := cur.(unwrapper)
		if !ok {
			break
		}
		cur = uw.Unwrap()
	}

	if flusher == nil {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch := h.broker.Subscribe()
	defer h.broker.Unsubscribe(ch)

	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			w.Write(msg) //nolint:errcheck
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
