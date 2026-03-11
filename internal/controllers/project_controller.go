// @nexus-project: nexus
// @nexus-path: internal/controllers/project_controller.go
// Package controllers contains domain controllers that operate on the state store.
// ProjectController manages entire projects as a unit —
// start, stop, and status for all services in a project at once.
package controllers

import (
	"fmt"
	"strings"
	"time"

	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	maxRecentFailuresThreshold = 3
	failureWindowMinutes       = 60
)

// ── MODELS ───────────────────────────────────────────────────────────────────

// ProjectStatus is the full health snapshot of a project.
type ProjectStatus struct {
	ProjectID   string
	ProjectName string
	Services    []*ServiceStatus
	StartedAt   time.Time
}

// ServiceStatus is the health snapshot of a single service within a project.
type ServiceStatus struct {
	ID           string
	Name         string
	DesiredState state.ServiceState
	ActualState  state.ServiceState
	Provider     state.ProviderType
	FailCount    int
	IsHealthy    bool
}

// ── CONTROLLER ───────────────────────────────────────────────────────────────

// ProjectController manages the lifecycle of entire projects.
// It never starts/stops services directly — it only sets desired state.
// The daemon reconciler reads desired state and acts on it.
type ProjectController struct {
	store *state.Store
	bus   *eventbus.Bus
}

// NewProjectController creates a ProjectController with required dependencies.
func NewProjectController(store *state.Store, bus *eventbus.Bus) *ProjectController {
	return &ProjectController{
		store: store,
		bus:   bus,
	}
}

// ── START ────────────────────────────────────────────────────────────────────

// StartProject sets desired_state = running for every service in a project.
// The daemon reconciler picks this up and starts each service.
// Returns the number of services that were queued to start.
func (c *ProjectController) StartProject(projectID string) (int, error) {
	project, err := c.store.GetProject(projectID)
	if err != nil {
		return 0, fmt.Errorf("get project: %w", err)
	}
	if project == nil {
		return 0, fmt.Errorf("project %q not found — register it first with: engx register <path>", projectID)
	}

	services, err := c.store.GetServicesByProject(projectID)
	if err != nil {
		return 0, fmt.Errorf("get services for project %s: %w", projectID, err)
	}
	if len(services) == 0 {
		return 0, fmt.Errorf("no services registered for project %q", projectID)
	}

	traceID := generateTraceID("start", projectID)
	started := 0

	for _, svc := range services {
		if svc.DesiredState == state.StateRunning {
			continue // already desired running — skip
		}

		if err := c.store.SetDesiredState(svc.ID, state.StateRunning); err != nil {
			return started, fmt.Errorf("set desired state for %s: %w", svc.ID, err)
		}

		c.bus.Publish(
			eventbus.TopicStateChanged,
			svc.ID,
			eventbus.StateChangedPayload{
				ServiceID: svc.ID,
				From:      string(svc.DesiredState),
				To:        string(state.StateRunning),
			},
		)

		if err := c.store.AppendEvent(
			svc.ID,
			state.EventStateChanged,
			state.SourceProjectCtrl,
			traceID,
			fmt.Sprintf(`{"action":"start","project":"%s","service":"%s"}`, projectID, svc.ID),
		); err != nil {
			return started, fmt.Errorf("append start event for %s: %w", svc.ID, err)
		}

		started++
	}

	return started, nil
}

// ── STOP ─────────────────────────────────────────────────────────────────────

// StopProject sets desired_state = stopped for every service in a project.
// The daemon reconciler picks this up and stops each service gracefully.
// Returns the number of services that were queued to stop.
func (c *ProjectController) StopProject(projectID string) (int, error) {
	project, err := c.store.GetProject(projectID)
	if err != nil {
		return 0, fmt.Errorf("get project: %w", err)
	}
	if project == nil {
		return 0, fmt.Errorf("project %q not found", projectID)
	}

	services, err := c.store.GetServicesByProject(projectID)
	if err != nil {
		return 0, fmt.Errorf("get services for project %s: %w", projectID, err)
	}

	traceID := generateTraceID("stop", projectID)
	stopped := 0

	for _, svc := range services {
		if svc.DesiredState == state.StateStopped {
			continue // already desired stopped — skip
		}

		if err := c.store.SetDesiredState(svc.ID, state.StateStopped); err != nil {
			return stopped, fmt.Errorf("set desired state for %s: %w", svc.ID, err)
		}

		c.bus.Publish(
			eventbus.TopicStateChanged,
			svc.ID,
			eventbus.StateChangedPayload{
				ServiceID: svc.ID,
				From:      string(svc.DesiredState),
				To:        string(state.StateStopped),
			},
		)

		if err := c.store.AppendEvent(
			svc.ID,
			state.EventStateChanged,
			state.SourceProjectCtrl,
			traceID,
			fmt.Sprintf(`{"action":"stop","project":"%s","service":"%s"}`, projectID, svc.ID),
		); err != nil {
			return stopped, fmt.Errorf("append stop event for %s: %w", svc.ID, err)
		}

		stopped++
	}

	return stopped, nil
}

// ── STATUS ───────────────────────────────────────────────────────────────────

// GetProjectStatus returns the full health snapshot of a project.
func (c *ProjectController) GetProjectStatus(projectID string) (*ProjectStatus, error) {
	project, err := c.store.GetProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	if project == nil {
		return nil, fmt.Errorf("project %q not found", projectID)
	}

	services, err := c.store.GetServicesByProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("get services for project %s: %w", projectID, err)
	}

	status := &ProjectStatus{
		ProjectID:   project.ID,
		ProjectName: project.Name,
		StartedAt:   time.Now().UTC(),
		Services:    make([]*ServiceStatus, 0, len(services)),
	}

	for _, svc := range services {
		recentFails, err := c.store.GetRecentFailures(svc.ID, failureWindowMinutes)
		if err != nil {
			return nil, fmt.Errorf("get recent failures for %s: %w", svc.ID, err)
		}

		isHealthy := svc.ActualState == state.StateRunning &&
			svc.DesiredState == state.StateRunning &&
			recentFails < maxRecentFailuresThreshold

		status.Services = append(status.Services, &ServiceStatus{
			ID:           svc.ID,
			Name:         svc.Name,
			DesiredState: svc.DesiredState,
			ActualState:  svc.ActualState,
			Provider:     svc.Provider,
			FailCount:    svc.FailCount,
			IsHealthy:    isHealthy,
		})
	}

	return status, nil
}

// ── ALL PROJECTS STATUS ───────────────────────────────────────────────────────

// GetAllProjectsStatus returns health snapshots for every registered project.
func (c *ProjectController) GetAllProjectsStatus() ([]*ProjectStatus, error) {
	projects, err := c.store.GetAllProjects()
	if err != nil {
		return nil, fmt.Errorf("get all projects: %w", err)
	}

	statuses := make([]*ProjectStatus, 0, len(projects))
	for _, project := range projects {
		status, err := c.GetProjectStatus(project.ID)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}

	return statuses, nil
}

// ── RENDER ───────────────────────────────────────────────────────────────────

// RenderStatus returns a formatted string ready to print to the terminal.
// Example output:
//
//	PROJECT: ums
//	  identity-api    running   running   docker   ✓
//	  gateway         running   stopped   process  ✗  (2 recent failures)
func (c *ProjectController) RenderStatus(status *ProjectStatus) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("\nPROJECT: %s\n", strings.ToUpper(status.ProjectID)))
	sb.WriteString(fmt.Sprintf("%-30s %-12s %-12s %-10s %s\n",
		"SERVICE", "DESIRED", "ACTUAL", "PROVIDER", "HEALTH"))
	sb.WriteString(strings.Repeat("─", 78) + "\n")

	for _, svc := range status.Services {
		health := "✓"
		if !svc.IsHealthy {
			health = "✗"
		}
		if svc.FailCount > 0 {
			health += fmt.Sprintf("  (%d failures)", svc.FailCount)
		}

		sb.WriteString(fmt.Sprintf("%-30s %-12s %-12s %-10s %s\n",
			svc.Name,
			string(svc.DesiredState),
			string(svc.ActualState),
			string(svc.Provider),
			health,
		))
	}

	total := len(status.Services)
	healthy := 0
	for _, svc := range status.Services {
		if svc.IsHealthy {
			healthy++
		}
	}
	sb.WriteString(fmt.Sprintf("\n%d/%d services healthy\n", healthy, total))

	return sb.String()
}

// ── HELPERS ──────────────────────────────────────────────────────────────────

func generateTraceID(action string, projectID string) string {
	return fmt.Sprintf("%s-%s-%d", action, projectID, time.Now().UnixNano())
}
