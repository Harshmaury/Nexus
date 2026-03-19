// @nexus-project: nexus
// @nexus-path: internal/api/handler/services_reset.go
// Reset handles POST /services/:id/reset (ADR-023).
// Clears a service from any stuck state (maintenance, crash loop) back
// to a clean stopped baseline so the reconciler will re-queue it.
//
// This is the startup grace mechanism — engx platform start calls reset
// on every service before queuing them, ensuring clean state on every boot
// regardless of fail counts accumulated in the previous session.
package handler

import (
	"fmt"
	"net/http"

	"github.com/Harshmaury/Nexus/internal/state"
)

// Reset handles POST /services/:id/reset (ADR-023).
// Resets actual_state=stopped, fail_count=0, last_failed_at=NULL,
// restart_after=NULL. Does not change desired_state. Idempotent.
func (h *ServicesHandler) Reset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondErr(w, http.StatusBadRequest, fmt.Errorf("service id required"))
		return
	}

	svc, err := h.store.GetService(id)
	if err != nil {
		respondErr(w, http.StatusInternalServerError,
			fmt.Errorf("get service %q: %w", id, err))
		return
	}
	if svc == nil {
		respondErr(w, http.StatusNotFound,
			fmt.Errorf("service %q not found", id))
		return
	}

	if err := resetServiceState(h.store, id); err != nil {
		respondErr(w, http.StatusInternalServerError, err)
		return
	}

	respondOK(w, map[string]any{"id": id, "reset": true})
}

// resetServiceState clears all stuck-state fields for a service.
// Called by the Reset handler and reusable by future batch reset logic.
func resetServiceState(store state.Storer, id string) error {
	if err := store.SetActualState(id, state.StateStopped); err != nil {
		return fmt.Errorf("reset actual state for %q: %w", id, err)
	}
	if err := store.ResetFailCount(id); err != nil {
		return fmt.Errorf("reset fail count for %q: %w", id, err)
	}
	if err := store.ClearRestartAfter(id); err != nil {
		return fmt.Errorf("clear restart after for %q: %w", id, err)
	}
	return nil
}
