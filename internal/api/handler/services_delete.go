// @nexus-project: nexus
// @nexus-path: internal/api/handler/services_delete.go
// ADR-033: DELETE /services/{id} HTTP endpoint.
// Removes a single service record from the DB.
// The service must be stopped (desired=stopped) before deletion.
package handler

import (
	"errors"
	"fmt"
	"net/http"
)

// Delete handles DELETE /services/{id}.
// Removes a single service from the DB. Returns 404 if not found.
// The caller is responsible for ensuring desired=stopped before calling.
func (h *ServicesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondErr(w, http.StatusBadRequest, errors.New("service id is required"))
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

	if err := h.store.DeleteService(id); err != nil {
		respondErr(w, http.StatusInternalServerError,
			fmt.Errorf("delete service %q: %w", id, err))
		return
	}

	respondOK(w, map[string]string{"id": id, "deleted": "true"})
}
