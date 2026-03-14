// @nexus-project: nexus
// @nexus-path: internal/api/middleware/recovery.go
package middleware

import (
	"log"
	"net/http"
	"runtime/debug"
)

// Recovery catches panics in handlers and returns a 500 instead of
// crashing the entire daemon process.
func Recovery(next http.Handler, logger *log.Logger) http.Handler {
	if logger == nil {
		logger = log.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				logger.Printf("api: panic in handler: %v\n%s", err, debug.Stack())
				http.Error(w, `{"ok":false,"error":"internal server error"}`,
					http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
