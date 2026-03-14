// @nexus-project: nexus
// @nexus-path: internal/api/handler/respond.go
// respond.go contains shared response helpers used by all handlers.
// Every handler uses these — never writes JSON manually.
package handler

import (
	"encoding/json"
	"net/http"
)

// apiResponse is the standard envelope for all API responses.
// Mirrors the socket protocol's Response shape for consistency.
type apiResponse struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// respondOK writes a 200 JSON response with data payload.
func respondOK(w http.ResponseWriter, data any) {
	respond(w, http.StatusOK, apiResponse{OK: true, Data: data})
}

// respondErr writes an error JSON response with the given HTTP status.
func respondErr(w http.ResponseWriter, status int, err error) {
	respond(w, status, apiResponse{OK: false, Error: err.Error()})
}

// respond is the single write path — sets Content-Type and encodes JSON.
func respond(w http.ResponseWriter, status int, body apiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body) //nolint:errcheck — best-effort write
}

// notImplemented returns 501 for handler stubs not yet implemented.
func notImplemented(w http.ResponseWriter) {
	respond(w, http.StatusNotImplemented, apiResponse{
		OK:    false,
		Error: "not implemented — coming in next phase8 script",
	})
}
