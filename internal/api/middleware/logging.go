// @nexus-project: nexus
// @nexus-path: internal/api/middleware/logging.go
// Package middleware provides HTTP middleware for the Nexus API server.
package middleware

import (
	"log"
	"net/http"
	"time"
)

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Logging logs method, path, status, and duration for every request.
// Format mirrors engxd's existing [engxd] log style.
func Logging(next http.Handler, logger *log.Logger) http.Handler {
	if logger == nil {
		logger = log.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		logger.Printf("api: %s %s → %d (%s)",
			r.Method, r.URL.Path, rw.status,
			time.Since(start).Round(time.Millisecond),
		)
	})
}
