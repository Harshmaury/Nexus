// @nexus-project: nexus
// @nexus-path: internal/plan/util.go
package plan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type contextKey string

const traceIDKey contextKey = "plan-trace-id"

// contextWithTraceID stores the plan trace ID in ctx for step propagation.
func contextWithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceIDFromContext retrieves the plan trace ID from ctx.
// Returns empty string if not set.
func TraceIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(traceIDKey).(string)
	return v
}

// randomHex generates n random bytes as a hex string (2n chars).
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b)
}
