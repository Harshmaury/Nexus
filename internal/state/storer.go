// @nexus-project: nexus
// @nexus-path: internal/state/storer.go
// Storer is the read/write contract for the Nexus state database.
// Wave 5: DeregisterProject, DeleteServicesByProject, DeleteService added (ADR-033).
package state

import "time"

// Storer is the read/write contract for the Nexus state database.
type Storer interface {
	// ── Lifecycle ────────────────────────────────────────────
	Close() error

	// ── Services ─────────────────────────────────────────────
	UpsertService(svc *Service) error
	GetService(id string) (*Service, error)
	GetAllServices() ([]*Service, error)
	GetServicesByProject(project string) ([]*Service, error)
	GetRunningServices() ([]*Service, error)
	DeleteService(id string) error                      // ADR-033: remove single service
	DeleteServicesByProject(projectID string) (int, error) // ADR-033: remove all services for a project

	// ── State ────────────────────────────────────────────────
	SetActualState(id string, s ServiceState) error
	SetDesiredState(id string, s ServiceState) error

	// ── Failure tracking ─────────────────────────────────────
	IncrementFailCount(id string) error
	ResetFailCount(id string) error
	GetRecentFailures(serviceID string, withinMinutes int) (int, error)

	// ── Back-off / recovery ──────────────────────────────────
	SetRestartAfter(id string, restartAt time.Time) error
	ClearRestartAfter(id string) error
	GetServicesReadyToRestart() ([]*Service, error)

	// ── Events ───────────────────────────────────────────────
	AppendEvent(serviceID string, eventType EventType, source EventSource, traceID, spanID, parentSpanID, component, level, outcome, payload string) error // ADR-037
	GetRecentEvents(limit int) ([]*Event, error)
	GetEventsByTrace(traceID string) ([]*Event, error)
	GetEventsSince(sinceID int64, limit int) ([]*Event, error)

	// ── Health ───────────────────────────────────────────────
	LogHealth(serviceID string, status ServiceState, exitCode int, message string) error

	// ── Projects ─────────────────────────────────────────────
	RegisterProject(p *Project) error
	GetProject(id string) (*Project, error)
	GetAllProjects() ([]*Project, error)
	DeregisterProject(id string) error // ADR-033

	// ── Download log ─────────────────────────────────────────
	LogDownload(d *DownloadLog) error
	GetRecentDownloads(limit int) ([]*DownloadLog, error)

	// ── Dependencies ─────────────────────────────────────────
	GetServiceDependencies(serviceID string) ([]string, error)
	SetServiceDependencies(serviceID string, deps []string) error

	// ── Agents ───────────────────────────────────────────────
	RegisterAgent(a *Agent) error
	HeartbeatAgent(agentID string) error
	GetAgent(id string) (*Agent, error)
	GetAllAgents() ([]*Agent, error)
	GetAgentToken(id string) (string, bool, error)
}
