// @nexus-project: nexus
// @nexus-path: internal/api/middleware/service_auth.go
// ServiceAuth validates inbound X-Service-Token headers from Atlas and Forge.
//
// ADR-008: inter-service authentication via pre-shared static tokens.
//
// The middleware is applied to all routes except /health.
// If the token table is empty (unauthenticated mode), all requests pass through
// with a one-time WARNING at startup — not per request, to avoid log spam.
//
// Token comparison uses crypto/subtle.ConstantTimeCompare to prevent
// timing-based token extraction (same pattern as agent token validation).
package middleware

import (
	"crypto/subtle"
	"log"
	"net/http"

	canon "github.com/Harshmaury/Canon/identity"
)

// serviceTokenHeader uses Canon canonical constant (ADR-016).
var serviceTokenHeader = canon.ServiceTokenHeader

// ServiceAuth returns a middleware that validates X-Service-Token on all
// requests except GET /health.
//
// tokens maps service name → expected token value.
// If tokens is empty, the middleware is a no-op and auth is skipped entirely
// (unauthenticated mode for local development without a service-tokens file).
func ServiceAuth(tokens map[string]string, logger *log.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = log.Default()
	}

	if len(tokens) == 0 {
		logger.Println("WARNING: no service-tokens file found — inter-service auth disabled")
		return func(next http.Handler) http.Handler { return next }
	}

	// Build a flat set of valid token bytes for O(n) lookup.
	// n is always ≤ 3 (atlas, forge, and future services).
	type entry struct {
		name  string
		token []byte
	}
	valid := make([]entry, 0, len(tokens))
	for name, tok := range tokens {
		valid = append(valid, entry{name: name, token: []byte(tok)})
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// /health is always exempt — monitoring tools must not need tokens.
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}

			incoming := []byte(r.Header.Get(serviceTokenHeader))
			if len(incoming) == 0 {
				http.Error(w, `{"ok":false,"error":"X-Service-Token required"}`,
					http.StatusUnauthorized)
				return
			}

			// ConstantTimeCompare — prevents timing-based token extraction.
			// Walk all entries regardless of early match to keep timing uniform.
			matched := false
			for _, e := range valid {
				if subtle.ConstantTimeCompare(e.token, incoming) == 1 {
					matched = true
					// Do not break — finish the loop to keep timing constant.
				}
			}

			if !matched {
				http.Error(w, `{"ok":false,"error":"invalid service token"}`,
					http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
