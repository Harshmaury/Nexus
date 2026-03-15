// @nexus-project: nexus
// @nexus-path: internal/state/storer.go
// Phase 13 addition:
//   GetRecentDownloads added to the interface.
//   *Store.GetRecentDownloads is implemented in db.go but was not
//   exposed via the interface. The train command reads download_log
//   through the daemon — this enables mock stores in tests.
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
	GetRecentDownloads(limit int) ([]*DownloadLog, error)

	// ── Dependencies ─────────────────────────────────────────
	GetServiceDependencies(serviceID string) ([]string, error)
	SetServiceDependencies(serviceID string, deps []string) error
}
