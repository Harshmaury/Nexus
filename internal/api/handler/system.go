// @nexus-project: nexus
// @nexus-path: internal/api/handler/system.go
// ADR-036: GET /system/graph — unified topology endpoint.
// ADR-038: POST /system/validate — pre-execution policy gate.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Harshmaury/Nexus/internal/state"
)

// SystemHandler handles /system routes.
type SystemHandler struct {
	store state.Storer
}

// NewSystemHandler creates a SystemHandler.
func NewSystemHandler(store state.Storer) *SystemHandler {
	return &SystemHandler{store: store}
}

// ── GRAPH TYPES (ADR-036) ─────────────────────────────────────────────────────

// GraphService is a service node in the system graph.
type GraphService struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Project      string   `json:"project"`
	Type         string   `json:"type"`
	DesiredState string   `json:"desired_state"`
	ActualState  string   `json:"actual_state"`
	DependsOn    []string `json:"depends_on"`
	FailCount    int      `json:"fail_count"`
}

// GraphProject is a project node in the system graph.
type GraphProject struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Language string   `json:"language"`
	Type     string   `json:"type"`
	Services []string `json:"services"`
}

// GraphEdge represents a dependency relationship between two services.
type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// SystemGraph is the full platform topology snapshot (ADR-036).
type SystemGraph struct {
	Services []GraphService `json:"services"`
	Projects []GraphProject `json:"projects"`
	Edges    []GraphEdge    `json:"edges"`
	AgentIDs []string       `json:"agents"`
}

// Graph handles GET /system/graph (ADR-036).
func (h *SystemHandler) Graph(w http.ResponseWriter, r *http.Request) {
	svcs, err := h.store.GetAllServices()
	if err != nil {
		respondErr(w, http.StatusInternalServerError, fmt.Errorf("get services: %w", err))
		return
	}
	projs, err := h.store.GetAllProjects()
	if err != nil {
		respondErr(w, http.StatusInternalServerError, fmt.Errorf("get projects: %w", err))
		return
	}
	agents, err := h.store.GetAllAgents()
	if err != nil {
		respondErr(w, http.StatusInternalServerError, fmt.Errorf("get agents: %w", err))
		return
	}

	// Build project → services index.
	projServices := map[string][]string{}
	for _, s := range svcs {
		projServices[s.Project] = append(projServices[s.Project], s.ID)
	}

	// Build graph services + dependency edges.
	var graphSvcs []GraphService
	var edges []GraphEdge
	for _, s := range svcs {
		deps, _ := h.store.GetServiceDependencies(s.ID)
		if deps == nil {
			deps = []string{}
		}
		graphSvcs = append(graphSvcs, GraphService{
			ID: s.ID, Name: s.Name, Project: s.Project,
			Type: string(s.Provider), DependsOn: deps,
			DesiredState: string(s.DesiredState), ActualState: string(s.ActualState),
			FailCount: s.FailCount,
		})
		for _, dep := range deps {
			edges = append(edges, GraphEdge{From: s.ID, To: dep})
		}
	}
	if graphSvcs == nil { graphSvcs = []GraphService{} }
	if edges == nil { edges = []GraphEdge{} }

	// Build graph projects.
	var graphProjs []GraphProject
	for _, p := range projs {
		svcIDs := projServices[p.ID]
		if svcIDs == nil { svcIDs = []string{} }
		graphProjs = append(graphProjs, GraphProject{
			ID: p.ID, Name: p.Name, Language: p.Language,
			Type: p.ProjectType, Services: svcIDs,
		})
	}
	if graphProjs == nil { graphProjs = []GraphProject{} }

	agentIDs := make([]string, 0, len(agents))
	for _, a := range agents { agentIDs = append(agentIDs, a.ID) }

	respondOK(w, SystemGraph{
		Services: graphSvcs, Projects: graphProjs,
		Edges: edges, AgentIDs: agentIDs,
	})
}

// ── VALIDATION TYPES (ADR-038) ────────────────────────────────────────────────

// ValidationViolation is a single policy violation.
type ValidationViolation struct {
	RuleID  string `json:"rule_id"`
	Message string `json:"message"`
	Action  string `json:"action"` // "deny" | "warn"
}

// ValidationResult is the response for POST /system/validate.
type ValidationResult struct {
	ProjectID  string                `json:"project_id"`
	Allowed    bool                  `json:"allowed"`
	Violations []ValidationViolation `json:"violations"`
}

// Validate handles POST /system/validate (ADR-038).
// Pre-execution policy gate — evaluates whether a project is safe to start.
func (h *SystemHandler) Validate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		Intent    string `json:"intent"` // "start" | "build" | "deploy"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErr(w, http.StatusBadRequest, fmt.Errorf("invalid request: %w", err))
		return
	}
	if req.ProjectID == "" {
		respondErr(w, http.StatusBadRequest, fmt.Errorf("project_id required"))
		return
	}

	proj, err := h.store.GetProject(req.ProjectID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err)
		return
	}
	if proj == nil {
		respondErr(w, http.StatusNotFound,
			fmt.Errorf("project %q not registered — run: engx register <path>", req.ProjectID))
		return
	}

	svcs, err := h.store.GetServicesByProject(req.ProjectID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err)
		return
	}

	var violations []ValidationViolation

	// V-001: project must have at least one registered service.
	if len(svcs) == 0 {
		violations = append(violations, ValidationViolation{
			RuleID: "V-001",
			Message: fmt.Sprintf("project %q has no services — run: engx register <path>", req.ProjectID),
			Action: "deny",
		})
	}

	// V-002: no service stuck in maintenance.
	for _, s := range svcs {
		if string(s.ActualState) == "maintenance" {
			violations = append(violations, ValidationViolation{
				RuleID: "V-002",
				Message: fmt.Sprintf("service %q in maintenance — run: engx services reset %s", s.ID, s.ID),
				Action: "warn",
			})
		}
	}

	// V-003: excessive fail count.
	for _, s := range svcs {
		if s.FailCount >= 5 {
			violations = append(violations, ValidationViolation{
				RuleID: "V-003",
				Message: fmt.Sprintf("service %q has %d failures — check: engx logs %s", s.ID, s.FailCount, s.ID),
				Action: "warn",
			})
		}
	}

	allowed := true
	for _, v := range violations {
		if v.Action == "deny" { allowed = false; break }
	}
	if violations == nil { violations = []ValidationViolation{} }

	respondOK(w, ValidationResult{
		ProjectID: req.ProjectID, Allowed: allowed, Violations: violations,
	})
}
