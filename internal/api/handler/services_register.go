// @nexus-project: nexus
// @nexus-path: internal/api/handler/services_register.go
// Register handles POST /services/register (ADR-022).
// Creates or updates a service record in the Nexus state store.
// This is the primary way to seed services — required before
// engx project start can queue any work.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Harshmaury/Nexus/internal/state"
)

// registerServiceRequest is the request body for POST /services/register.
type registerServiceRequest struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Project  string `json:"project"`
	Provider string `json:"provider"`
	Config   string `json:"config"`
}

// Register handles POST /services/register (ADR-022).
// Upserts a service record — creates on first call, updates config on repeat.
// Sets desired_state=stopped and actual_state=stopped on initial creation.
func (h *ServicesHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErr(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if err := validateServiceRequest(req); err != nil {
		respondErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" {
		req.Name = req.ID
	}

	svc := &state.Service{
		ID:           req.ID,
		Name:         req.Name,
		Project:      req.Project,
		Provider:     state.ProviderType(req.Provider),
		Config:       req.Config,
		DesiredState: state.StateStopped,
		ActualState:  state.StateStopped,
	}

	if err := h.store.UpsertService(svc); err != nil {
		respondErr(w, http.StatusInternalServerError,
			fmt.Errorf("register service: %w", err))
		return
	}

	respondOK(w, map[string]string{"id": req.ID, "name": req.Name})
}

// validateServiceRequest checks required fields and provider validity.
func validateServiceRequest(req registerServiceRequest) error {
	if req.ID == "" {
		return errors.New("id is required")
	}
	if req.Project == "" {
		return errors.New("project is required")
	}
	if req.Provider == "" {
		return errors.New("provider is required")
	}
	switch state.ProviderType(req.Provider) {
	case state.ProviderProcess, state.ProviderDocker, state.ProviderK8s:
		// valid
	default:
		return fmt.Errorf("unknown provider %q — must be one of: process, docker, k8s", req.Provider)
	}
	if req.Config == "" {
		return errors.New("config is required")
	}
	return nil
}
