// @nexus-project: nexus
// @nexus-path: internal/api/handler/services.go
// ServicesHandler handles all /services routes.
// Handlers are thin adapters — parse → store query → respond.
package handler

import (
	"fmt"
	"net/http"

	"github.com/Harshmaury/Nexus/internal/state"
)

// ServicesHandler handles all /services routes.
type ServicesHandler struct {
	store state.Storer
}

// NewServicesHandler creates a ServicesHandler.
func NewServicesHandler(store state.Storer) *ServicesHandler {
	return &ServicesHandler{store: store}
}

// List handles GET /services
// Returns every registered service with its current desired and actual state.
// Supports optional query params:
//
//	?project=<id>  filter by project ID
func (h *ServicesHandler) List(w http.ResponseWriter, r *http.Request) {
	projectFilter := r.URL.Query().Get("project")

	if projectFilter != "" {
		services, err := h.store.GetServicesByProject(projectFilter)
		if err != nil {
			respondErr(w, http.StatusInternalServerError,
				fmt.Errorf("get services for project %q: %w", projectFilter, err))
			return
		}
		respondOK(w, services)
		return
	}

	services, err := h.store.GetAllServices()
	if err != nil {
		respondErr(w, http.StatusInternalServerError,
			fmt.Errorf("get services: %w", err))
		return
	}
	respondOK(w, services)
}
