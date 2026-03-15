// @nexus-project: nexus
// @nexus-path: internal/api/handler/agents.go
// NX-Fix-05: validateToken now uses subtle.ConstantTimeCompare.
//   Plain string equality (a.Token != incoming) is vulnerable to timing
//   attacks — an attacker on a local network can measure response latency
//   to extract the token one byte at a time. ConstantTimeCompare always
//   takes the same time regardless of where the strings first differ.
//
// AgentsHandler handles all /agents routes.
// These are called exclusively by remote engxa agents — not by the CLI.
//
// Routes:
//   POST /agents/register          — engxa calls on startup
//   POST /agents/:id/heartbeat     — engxa calls every 10s
//   GET  /agents/:id/desired       — engxa polls for desired service states
//   POST /agents/:id/actual        — engxa reports actual service states
//   GET  /agents                   — CLI: list all registered agents
package handler

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Harshmaury/Nexus/internal/agent"
	"github.com/Harshmaury/Nexus/internal/state"
)

// AgentsHandler handles all /agents routes.
type AgentsHandler struct {
	store state.Storer
}

// NewAgentsHandler creates an AgentsHandler.
func NewAgentsHandler(store state.Storer) *AgentsHandler {
	return &AgentsHandler{store: store}
}

// ── LIST ──────────────────────────────────────────────────────────────────────

// List handles GET /agents
// Returns all registered agents with online/offline status.
func (h *AgentsHandler) List(w http.ResponseWriter, r *http.Request) {
	agents, err := h.store.GetAllAgents()
	if err != nil {
		respondErr(w, http.StatusInternalServerError, fmt.Errorf("get agents: %w", err))
		return
	}

	type agentView struct {
		ID           string `json:"id"`
		Hostname     string `json:"hostname"`
		Address      string `json:"address"`
		Online       bool   `json:"online"`
		LastSeen     string `json:"last_seen,omitempty"`
		RegisteredAt string `json:"registered_at"`
	}

	views := make([]agentView, 0, len(agents))
	for _, a := range agents {
		v := agentView{
			ID:           a.ID,
			Hostname:     a.Hostname,
			Address:      a.Address,
			Online:       a.Online,
			RegisteredAt: a.RegisteredAt.Format("2006-01-02 15:04:05"),
		}
		if !a.LastSeen.IsZero() {
			v.LastSeen = a.LastSeen.Format("2006-01-02 15:04:05")
		}
		views = append(views, v)
	}

	respondOK(w, views)
}

// ── REGISTER ─────────────────────────────────────────────────────────────────

// Register handles POST /agents/register
// Called by engxa on startup. Creates or updates the agent record.
func (h *AgentsHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string `json:"id"`
		Hostname string `json:"hostname"`
		Address  string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErr(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}
	if req.ID == "" {
		respondErr(w, http.StatusBadRequest, fmt.Errorf("id required"))
		return
	}

	// Token comes from X-Nexus-Token header — stored on registration so the
	// server can validate future requests from this agent.
	token := r.Header.Get("X-Nexus-Token")

	if err := h.store.RegisterAgent(&state.Agent{
		ID:       req.ID,
		Hostname: req.Hostname,
		Address:  req.Address,
		Token:    token,
	}); err != nil {
		respondErr(w, http.StatusInternalServerError, fmt.Errorf("register agent: %w", err))
		return
	}

	respondOK(w, map[string]string{"id": req.ID, "status": "registered"})
}

// ── HEARTBEAT ────────────────────────────────────────────────────────────────

// Heartbeat handles POST /agents/:id/heartbeat
// Updates last_seen. Returns 404 if agent not registered yet.
func (h *AgentsHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondErr(w, http.StatusBadRequest, fmt.Errorf("agent id required"))
		return
	}

	if !h.validateToken(w, r, id) {
		return
	}

	if err := h.store.HeartbeatAgent(id); err != nil {
		respondErr(w, http.StatusInternalServerError, fmt.Errorf("heartbeat: %w", err))
		return
	}

	respondOK(w, map[string]string{"status": "ok"})
}

// ── DESIRED STATE ─────────────────────────────────────────────────────────────

// Desired handles GET /agents/:id/desired
// Returns all services assigned to this agent with their desired states.
// Services are matched by project — projects registered with agent_id=<id>
// in their config_json are routed to that agent.
func (h *AgentsHandler) Desired(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondErr(w, http.StatusBadRequest, fmt.Errorf("agent id required"))
		return
	}

	if !h.validateToken(w, r, id) {
		return
	}

	// Fetch all services and filter to those assigned to this agent.
	// Convention: svc.Config JSON may contain "agent_id": "<id>" to pin
	// a service to a specific agent. If absent, the service runs centrally.
	services, err := h.store.GetAllServices()
	if err != nil {
		respondErr(w, http.StatusInternalServerError, fmt.Errorf("get services: %w", err))
		return
	}

	desired := make([]agent.ServiceDesiredState, 0)
	for _, svc := range services {
		if !serviceAssignedToAgent(svc.Config, id) {
			continue
		}
		desired = append(desired, agent.ServiceDesiredState{
			ServiceID:    svc.ID,
			DesiredState: string(svc.DesiredState),
			Provider:     string(svc.Provider),
			Config:       svc.Config,
			Name:         svc.Name,
			Project:      svc.Project,
		})
	}

	respondOK(w, desired)
}

// ── ACTUAL STATE ──────────────────────────────────────────────────────────────

// Actual handles POST /agents/:id/actual
// The agent reports actual running state. The server updates actual_state
// in the services table. This keeps the central state store authoritative.
func (h *AgentsHandler) Actual(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondErr(w, http.StatusBadRequest, fmt.Errorf("agent id required"))
		return
	}

	if !h.validateToken(w, r, id) {
		return
	}

	var actuals []agent.ServiceActualState
	if err := json.NewDecoder(r.Body).Decode(&actuals); err != nil {
		respondErr(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	updated := 0
	for _, a := range actuals {
		if a.ServiceID == "" {
			continue
		}
		if err := h.store.SetActualState(a.ServiceID, state.ServiceState(a.ActualState)); err != nil {
			// Non-fatal — log and continue with remaining services.
			continue
		}
		updated++
	}

	respondOK(w, map[string]any{"updated": updated})
}

// ── TOKEN VALIDATION ──────────────────────────────────────────────────────────

// validateToken checks X-Nexus-Token against the stored agent token.
// Returns false and writes a 401 if validation fails.
func (h *AgentsHandler) validateToken(w http.ResponseWriter, r *http.Request, agentID string) bool {
	incoming := r.Header.Get("X-Nexus-Token")
	if incoming == "" {
		respondErr(w, http.StatusUnauthorized, fmt.Errorf("X-Nexus-Token header required"))
		return false
	}

	a, err := h.store.GetAgent(agentID)
	if err != nil || a == nil {
		respondErr(w, http.StatusNotFound, fmt.Errorf("agent %q not registered", agentID))
		return false
	}

	// subtle.ConstantTimeCompare prevents timing-based token extraction.
	// Both slices are compared in constant time regardless of where they differ.
	if subtle.ConstantTimeCompare([]byte(a.Token), []byte(incoming)) != 1 {
		respondErr(w, http.StatusUnauthorized, fmt.Errorf("invalid token"))
		return false
	}

	return true
}

// ── HELPERS ───────────────────────────────────────────────────────────────────

// serviceAssignedToAgent returns true if svc.Config JSON contains
// "agent_id": "<agentID>". Services without agent_id run centrally.
func serviceAssignedToAgent(configJSON string, agentID string) bool {
	if configJSON == "" || configJSON == "{}" {
		return false
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return false
	}
	v, ok := cfg["agent_id"]
	if !ok {
		return false
	}
	s, ok := v.(string)
	return ok && s == agentID
}
