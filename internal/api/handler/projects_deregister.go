// @nexus-project: nexus
// @nexus-path: internal/api/handler/projects_deregister.go
// ADR-033: DELETE /projects/{id} HTTP endpoint.
// Complements the socket CmdDeregisterProject — allows herald and any
// HTTP client to deregister a project without needing the daemon socket.
// Stops all services, deletes services, deletes project. Idempotent on 404.
package handler

import (
	"errors"
	"fmt"
	"net/http"
)

// Delete handles DELETE /projects/{id} (ADR-033).
// Stops all services in the project, removes services and project from DB.
// Returns 404 if the project does not exist (idempotent — safe to retry).
func (h *ProjectsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondErr(w, http.StatusBadRequest, errors.New("project id is required"))
		return
	}

	// Verify project exists before attempting deregister.
	proj, err := h.store.GetProject(id)
	if err != nil {
		respondErr(w, http.StatusInternalServerError,
			fmt.Errorf("get project %q: %w", id, err))
		return
	}
	if proj == nil {
		respondErr(w, http.StatusNotFound,
			fmt.Errorf("project %q not found", id))
		return
	}

	// Stop all services first — set desired=stopped so reconciler doesn't restart them.
	if _, err := h.ctrl.StopProject(id); err != nil {
		// Non-fatal if project has no services — continue with deletion.
		if !isNotFound(err) {
			respondErr(w, http.StatusInternalServerError,
				fmt.Errorf("stop project %q: %w", id, err))
			return
		}
	}

	// Delete all services belonging to this project.
	servicesRemoved, err := h.store.DeleteServicesByProject(id)
	if err != nil {
		respondErr(w, http.StatusInternalServerError,
			fmt.Errorf("delete services for %q: %w", id, err))
		return
	}

	// Delete the project record.
	if err := h.store.DeregisterProject(id); err != nil {
		respondErr(w, http.StatusInternalServerError,
			fmt.Errorf("deregister project %q: %w", id, err))
		return
	}

	respondOK(w, map[string]any{
		"id":               id,
		"services_removed": servicesRemoved,
	})
}
