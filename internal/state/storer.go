// @nexus-project: nexus
// @nexus-path: internal/state/storer.go
// Storer is the interface all controllers and the reconciler depend on.
// *state.Store satisfies this interface automatically (duck typing).
// Tests supply a mock; Phase 8 HTTP handlers do the same.
//
// Why Storer, not Store?
//   db.go already declares: type Store struct { ... }
//   Go does not allow a type and interface with the same name in the
//   same package. Idiomatic convention: -er suffix → state.Storer.
//
// Fix: Added LogDownload to the interface.
//   *Store.LogDownload is implemented in db.go (line ~500) but was
//   missing from this interface. DropLogger calls store.LogDownload —
//   once logger.go was corrected to accept state.Storer instead of
//   *state.Store, the build would fail at compile time without this.
package state

import "time"

// Storer is the read/write contract for the Nexus state database.
// Controllers and the reconciler depend on this interface, never on
// the concrete *Store type directly.
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
	// LogDownload records a file processed by Nexus Drop.
	// Required by DropLogger in internal/intelligence/logger.go.
	LogDownload(d *DownloadLog) error
}
