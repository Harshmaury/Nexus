// @nexus-project: nexus
// @nexus-path: internal/state/storer.go
// Storer is the interface all controllers and the reconciler depend on.
// *state.Store satisfies this interface automatically (duck typing).
// Tests supply a mock; Phase 8 HTTP handlers do the same.
//
// Phase 11 addition:
//   GetServiceDependencies — returns the depends_on list for a service
//   SetServiceDependencies — writes the depends_on list for a service
//   Both are used by the reconciler engine for topological sort.
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
	AppendEvent(serviceID string, eventType EventType, source EventSource, traceID string, payload string) error
	GetRecentEvents(limit int) ([]*Event, error)
	GetEventsByTrace(traceID string) ([]*Event, error)

	// ── Health ───────────────────────────────────────────────
	LogHealth(serviceID string, status ServiceState, exitCode int, message string) error

	// ── Projects ─────────────────────────────────────────────
	RegisterProject(p *Project) error
	GetProject(id string) (*Project, error)
	GetAllProjects() ([]*Project, error)

	// ── Download log ─────────────────────────────────────────
	LogDownload(d *DownloadLog) error

	// ── Dependencies (Phase 11) ───────────────────────────────
	// GetServiceDependencies returns the IDs of services that must be
	// running before serviceID is started. Returns nil if none declared.
	GetServiceDependencies(serviceID string) ([]string, error)

	// SetServiceDependencies writes the depends_on list for a service.
	// Called by engx register when .nexus.yaml declares depends_on.
	SetServiceDependencies(serviceID string, deps []string) error
}
