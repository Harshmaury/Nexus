// @nexus-project: nexus
// @nexus-path: internal/api/handler/mode.go
// ADR-044: GET /system/mode — exposes runtime mode and capability status.
// Used by engx doctor, engx status, and future Relay identity probe.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/Harshmaury/Nexus/internal/mode"
)

// ModeHandler handles GET /system/mode.
type ModeHandler struct {
	evaluator *mode.Evaluator
}

// NewModeHandler creates a ModeHandler.
func NewModeHandler(e *mode.Evaluator) *ModeHandler {
	return &ModeHandler{evaluator: e}
}

// Get handles GET /system/mode.
// Returns the current runtime mode and full capability list.
// Re-evaluates on every call so the response always reflects
// the live state of the platform.
func (h *ModeHandler) Get(w http.ResponseWriter, r *http.Request) {
	// Re-evaluate live — mode can change as services come up/down.
	snap := h.evaluator.Evaluate()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(struct { //nolint:errcheck
		OK   bool             `json:"ok"`
		Data mode.Snapshot    `json:"data"`
	}{OK: true, Data: snap})
}
