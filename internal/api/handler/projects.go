// @nexus-project: nexus
// @nexus-path: internal/api/handler/projects.go
// ProjectsHandler handles all /projects routes.
//
// Architecture contract — handlers are thin adapters only:
//   parse request → call controller method → respond with result
//
// No business logic lives here. All decisions are in ProjectController.
// Error → HTTP status mapping is the only judgement handlers make.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ProjectsHandler handles all /projects routes.
type ProjectsHandler struct {
	ctrl  *controllers.ProjectController
	store state.Storer
}

// NewProjectsHandler creates a ProjectsHandler with required dependencies.
func NewProjectsHandler(ctrl *controllers.ProjectController, store state.Storer) *ProjectsHandler {
	return &ProjectsHandler{ctrl: ctrl, store: store}
}

// ── LIST ──────────────────────────────────────────────────────────────────────

// List handles GET /projects
// Returns health snapshots for every registered project.
func (h *ProjectsHandler) List(w http.ResponseWriter, r *http.Request) {
	statuses, err := h.ctrl.GetAllProjectsStatus()
	if err != nil {
		respondErr(w, http.StatusInternalServerError, fmt.Errorf("list projects: %w", err))
		return
	}
	respondOK(w, statuses)
}

// ── GET ───────────────────────────────────────────────────────────────────────

// Get handles GET /projects/{id}
// Returns the full health snapshot of a single project.
func (h *ProjectsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondErr(w, http.StatusBadRequest, errors.New("project id is required"))
		return
	}

	status, err := h.ctrl.GetProjectStatus(id)
	if err != nil {
		if isNotFound(err) {
			respondErr(w, http.StatusNotFound, err)
			return
		}
		respondErr(w, http.StatusInternalServerError, fmt.Errorf("get project: %w", err))
		return
	}

	respondOK(w, status)
}

// ── START ─────────────────────────────────────────────────────────────────────

// Start handles POST /projects/{id}/start
// Sets desired_state = running for all services in the project.
// The reconciler picks up the change on its next tick.
func (h *ProjectsHandler) Start(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondErr(w, http.StatusBadRequest, errors.New("project id is required"))
		return
	}

	count, err := h.ctrl.StartProject(id)
	if err != nil {
		if isNotFound(err) {
			respondErr(w, http.StatusNotFound, err)
			return
		}
		respondErr(w, http.StatusInternalServerError, fmt.Errorf("start project: %w", err))
		return
	}

	respondOK(w, map[string]any{
		"project_id": id,
		"queued":     count,
		"message":    startMessage(id, count),
	})
}

// ── STOP ──────────────────────────────────────────────────────────────────────

// Stop handles POST /projects/{id}/stop
// Sets desired_state = stopped for all services in the project.
func (h *ProjectsHandler) Stop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondErr(w, http.StatusBadRequest, errors.New("project id is required"))
		return
	}

	count, err := h.ctrl.StopProject(id)
	if err != nil {
		if isNotFound(err) {
			respondErr(w, http.StatusNotFound, err)
			return
		}
		respondErr(w, http.StatusInternalServerError, fmt.Errorf("stop project: %w", err))
		return
	}

	respondOK(w, map[string]any{
		"project_id": id,
		"queued":     count,
		"message":    stopMessage(id, count),
	})
}

// ── REGISTER ──────────────────────────────────────────────────────────────────

// registerProjectRequest is the JSON body for POST /projects/register.
type registerProjectRequest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Language    string `json:"language"`
	ProjectType string `json:"project_type"`
	ConfigJSON  string `json:"config_json"`
}

// Register handles POST /projects/register
// Registers a new project in the state store.
// The daemon is the single writer — HTTP and socket share the same store.
func (h *ProjectsHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErr(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}

	if req.Name == "" {
		respondErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	if req.ID == "" {
		// Derive ID from name if not provided — same logic as engx CLI
		req.ID = strings.ToLower(strings.ReplaceAll(req.Name, " ", "-"))
	}

	project := &state.Project{
		ID:          req.ID,
		Name:        req.Name,
		Path:        req.Path,
		Language:    req.Language,
		ProjectType: req.ProjectType,
		ConfigJSON:  req.ConfigJSON,
	}

	if err := h.store.RegisterProject(project); err != nil {
		respondErr(w, http.StatusInternalServerError, fmt.Errorf("register project: %w", err))
		return
	}

	respondOK(w, map[string]string{
		"id":   req.ID,
		"name": req.Name,
	})
}

// ── HELPERS ───────────────────────────────────────────────────────────────────

// isNotFound returns true if err contains a "not found" message.
// Avoids importing a sentinel error from the controllers package.
func isNotFound(err error) bool {
	return strings.Contains(err.Error(), "not found")
}

func startMessage(id string, queued int) string {
	if queued == 0 {
		return fmt.Sprintf("all services in %q already running", id)
	}
	return fmt.Sprintf("queued %d service(s) to start — daemon will reconcile", queued)
}

func stopMessage(id string, queued int) string {
	if queued == 0 {
		return fmt.Sprintf("all services in %q already stopped", id)
	}
	return fmt.Sprintf("queued %d service(s) to stop", queued)
}
