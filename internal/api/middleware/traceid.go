// @nexus-project: nexus
// @nexus-path: internal/api/middleware/traceid.go
// TraceID middleware implements X-Trace-ID propagation for Nexus Phase 15.
//
// Every inbound HTTP request gets a trace ID:
//   - If X-Trace-ID header is present (Atlas or Forge forwarding it), use it.
//   - Otherwise generate a new one: nexus-<unix-nano>.
//
// The trace ID is stored in the request context so handlers can attach
// it to events they write. It is also echoed back in the response header
// so callers can correlate their logs with the Nexus event log.
//
// ADR-008 note: X-Trace-ID is not an auth header. It rides alongside
// X-Service-Token and is always accepted — even on /health.
//
// Canon compliance: TraceIDHeader imported from identity — never redefined locally.
package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Harshmaury/Canon/identity"
)

// traceIDKey is the unexported context key for the trace ID.
type traceIDKey struct{}

const traceIDPrefix = "nexus"

// TraceID returns middleware that ensures every request carries a trace ID.
// If the inbound request already has X-Trace-ID, it is reused.
// Otherwise a new ID is generated. The ID is stored in context and
// echoed in the response header.
func TraceID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(identity.TraceIDHeader)
		if id == "" {
			id = newTraceID()
		}

		ctx := context.WithValue(r.Context(), traceIDKey{}, id)
		w.Header().Set(identity.TraceIDHeader, id)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TraceIDFromContext extracts the trace ID from a context.
// Returns an empty string if none is present.
func TraceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(traceIDKey{}).(string)
	return id
}

// newTraceID generates a unique trace ID scoped to the Nexus service.
func newTraceID() string {
	return fmt.Sprintf("%s-%d", traceIDPrefix, time.Now().UnixNano())
}
