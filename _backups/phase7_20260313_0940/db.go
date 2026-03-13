// @nexus-project: nexus
// @nexus-path: internal/state/db.go
// Package state manages the SQLite source of truth for the Nexus daemon.
// All desired and actual service states are persisted here.
// The daemon queries this on every reconcile cycle.
package state

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

// ServiceState represents the lifecycle state of a managed service.
type ServiceState string

const (
	StateRunning     ServiceState = "running"
	StateStopped     ServiceState = "stopped"
	StateCrashed     ServiceState = "crashed"
	StateRecovering  ServiceState = "recovering"
	StateMaintenance ServiceState = "maintenance"
	StateUnknown     ServiceState = "unknown"
)

// ProviderType identifies which runtime manages a service.
type ProviderType string

const (
	ProviderDocker  ProviderType = "docker"
	ProviderK8s     ProviderType = "k8s"
	ProviderProcess ProviderType = "process"
)

// EventType represents things that happen in the platform.
type EventType string

const (
	EventServiceStarted EventType = "SERVICE_STARTED"
	EventServiceStopped EventType = "SERVICE_STOPPED"
	EventServiceCrashed EventType = "SERVICE_CRASHED"
	EventServiceHealed  EventType = "SERVICE_HEALED"
	EventStateChanged   EventType = "STATE_CHANGED"
	EventSystemAlert    EventType = "SYSTEM_ALERT"
	EventFileDropped    EventType = "FILE_DROPPED"
	EventFileRouted     EventType = "FILE_ROUTED"
)

// EventSource identifies which component emitted an event.
type EventSource string

const (
	SourceDaemon        EventSource = "daemon"
	SourceDockerPlugin  EventSource = "docker-plugin"
	SourceK8sPlugin     EventSource = "k8s-plugin"
	SourceHealthPlugin  EventSource = "health-plugin"
	SourceRecovery      EventSource = "recovery-plugin"
	SourceDropSystem    EventSource = "drop-system"
	SourceCLI           EventSource = "cli"
	SourceProjectCtrl   EventSource = "project-controller"
)

// ── MODELS ───────────────────────────────────────────────────────────────────

// Service is the core entity — one row per managed service.
type Service struct {
	ID           string
	Name         string
	Project      string
	DesiredState ServiceState
	ActualState  ServiceState
	Provider     ProviderType
	Config       string
	FailCount    int
	LastFailedAt *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Event is an immutable log of everything that happened.
// source — which component emitted it (docker-plugin, daemon, cli...)
// trace_id — groups related events across a single operation (e.g. crash→recover→heal)
type Event struct {
	ID        int64
	ServiceID string
	Type      EventType
	Source    EventSource
	TraceID   string
	Payload   string
	CreatedAt time.Time
}

// HealthLog records every health check result.
type HealthLog struct {
	ID        int64
	ServiceID string
	Status    ServiceState
	ExitCode  int
	Message   string
	CheckedAt time.Time
}

// DownloadLog records every file processed by Nexus Drop.
type DownloadLog struct {
	ID           int64
	OriginalName string
	RenamedTo    string
	Project      string
	Source       string
	Destination  string
	Method       string
	Action       string    // moved | prompted | tagged | skipped
	Confidence   float64
	DownloadedAt time.Time
}

// Project is a registered project manifest.
type Project struct {
	ID           string
	Name         string
	Path         string
	Language     string
	ProjectType  string
	ConfigJSON   string
	RegisteredAt time.Time
	UpdatedAt    time.Time
}

// ── STORE ────────────────────────────────────────────────────────────────────

// Store is the single source of truth for the Nexus daemon.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the SQLite database at the given path.
// Runs all migrations automatically on first open.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite performs best with a single writer — prevent locking errors.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	store := &Store{db: db}

	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return store, nil
}

// Close shuts down the database connection cleanly.
func (s *Store) Close() error {
	return s.db.Close()
}

// ── SERVICE OPERATIONS ───────────────────────────────────────────────────────

// UpsertService inserts or updates a service record.
func (s *Store) UpsertService(svc *Service) error {
	query := `
		INSERT INTO services (
			id, name, project, desired_state, actual_state,
			provider, config, fail_count, last_failed_at,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name           = excluded.name,
			desired_state  = excluded.desired_state,
			actual_state   = excluded.actual_state,
			provider       = excluded.provider,
			config         = excluded.config,
			fail_count     = excluded.fail_count,
			last_failed_at = excluded.last_failed_at,
			updated_at     = excluded.updated_at
	`
	now := time.Now().UTC()
	_, err := s.db.Exec(query,
		svc.ID, svc.Name, svc.Project,
		svc.DesiredState, svc.ActualState,
		svc.Provider, svc.Config,
		svc.FailCount, svc.LastFailedAt,
		now, now,
	)
	if err != nil {
		return fmt.Errorf("upsert service %s: %w", svc.ID, err)
	}
	return nil
}

// GetService returns a single service by ID.
func (s *Store) GetService(id string) (*Service, error) {
	query := `
		SELECT id, name, project, desired_state, actual_state,
		       provider, config, fail_count, last_failed_at,
		       created_at, updated_at
		FROM services WHERE id = ?
	`
	row := s.db.QueryRow(query, id)
	return scanService(row)
}

// GetAllServices returns every registered service.
func (s *Store) GetAllServices() ([]*Service, error) {
	query := `
		SELECT id, name, project, desired_state, actual_state,
		       provider, config, fail_count, last_failed_at,
		       created_at, updated_at
		FROM services ORDER BY project, name
	`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query all services: %w", err)
	}
	defer rows.Close()
	return scanServices(rows)
}

// GetServicesByProject returns all services for a given project.
func (s *Store) GetServicesByProject(project string) ([]*Service, error) {
	query := `
		SELECT id, name, project, desired_state, actual_state,
		       provider, config, fail_count, last_failed_at,
		       created_at, updated_at
		FROM services WHERE project = ? ORDER BY name
	`
	rows, err := s.db.Query(query, project)
	if err != nil {
		return nil, fmt.Errorf("query services for project %s: %w", project, err)
	}
	defer rows.Close()
	return scanServices(rows)
}

// GetRunningServices returns all services with desired_state = running.
func (s *Store) GetRunningServices() ([]*Service, error) {
	query := `
		SELECT id, name, project, desired_state, actual_state,
		       provider, config, fail_count, last_failed_at,
		       created_at, updated_at
		FROM services WHERE desired_state = ? ORDER BY project, name
	`
	rows, err := s.db.Query(query, StateRunning)
	if err != nil {
		return nil, fmt.Errorf("query running services: %w", err)
	}
	defer rows.Close()
	return scanServices(rows)
}

// SetActualState updates only the actual (runtime) state of a service.
func (s *Store) SetActualState(id string, state ServiceState) error {
	_, err := s.db.Exec(
		`UPDATE services SET actual_state = ?, updated_at = ? WHERE id = ?`,
		state, time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("set actual state for %s: %w", id, err)
	}
	return nil
}

// SetDesiredState updates the desired state — what the reconciler will aim for.
func (s *Store) SetDesiredState(id string, state ServiceState) error {
	_, err := s.db.Exec(
		`UPDATE services SET desired_state = ?, updated_at = ? WHERE id = ?`,
		state, time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("set desired state for %s: %w", id, err)
	}
	return nil
}

// IncrementFailCount increments the failure counter and records the time.
func (s *Store) IncrementFailCount(id string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE services SET fail_count = fail_count + 1, last_failed_at = ?, updated_at = ? WHERE id = ?`,
		now, now, id,
	)
	if err != nil {
		return fmt.Errorf("increment fail count for %s: %w", id, err)
	}
	return nil
}

// ResetFailCount clears the failure counter after a successful recovery.
func (s *Store) ResetFailCount(id string) error {
	_, err := s.db.Exec(
		`UPDATE services SET fail_count = 0, last_failed_at = NULL, updated_at = ? WHERE id = ?`,
		time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("reset fail count for %s: %w", id, err)
	}
	return nil
}

// ── EVENT OPERATIONS ─────────────────────────────────────────────────────────

// AppendEvent writes an immutable event with source and optional trace ID.
func (s *Store) AppendEvent(serviceID string, eventType EventType, source EventSource, traceID string, payload string) error {
	_, err := s.db.Exec(
		`INSERT INTO events (service_id, type, source, trace_id, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		serviceID, eventType, source, traceID, payload, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("append event %s for %s: %w", eventType, serviceID, err)
	}
	return nil
}

// GetRecentEvents returns the N most recent events.
func (s *Store) GetRecentEvents(limit int) ([]*Event, error) {
	rows, err := s.db.Query(
		`SELECT id, service_id, type, source, trace_id, payload, created_at
		 FROM events ORDER BY created_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent events: %w", err)
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		e := &Event{}
		if err := rows.Scan(&e.ID, &e.ServiceID, &e.Type, &e.Source, &e.TraceID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// GetEventsByTrace returns all events sharing a trace ID — full operation history.
func (s *Store) GetEventsByTrace(traceID string) ([]*Event, error) {
	rows, err := s.db.Query(
		`SELECT id, service_id, type, source, trace_id, payload, created_at
		 FROM events WHERE trace_id = ? ORDER BY created_at ASC`,
		traceID,
	)
	if err != nil {
		return nil, fmt.Errorf("query events by trace %s: %w", traceID, err)
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		e := &Event{}
		if err := rows.Scan(&e.ID, &e.ServiceID, &e.Type, &e.Source, &e.TraceID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ── HEALTH LOG OPERATIONS ────────────────────────────────────────────────────

// LogHealth records a health check result.
func (s *Store) LogHealth(serviceID string, status ServiceState, exitCode int, message string) error {
	_, err := s.db.Exec(
		`INSERT INTO health_logs (service_id, status, exit_code, message, checked_at) VALUES (?, ?, ?, ?, ?)`,
		serviceID, status, exitCode, message, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("log health for %s: %w", serviceID, err)
	}
	return nil
}

// GetRecentFailures counts crashes for a service within the last N minutes.
func (s *Store) GetRecentFailures(serviceID string, withinMinutes int) (int, error) {
	since := time.Now().UTC().Add(-time.Duration(withinMinutes) * time.Minute)
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM health_logs WHERE service_id = ? AND status = ? AND checked_at > ?`,
		serviceID, StateCrashed, since,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count recent failures for %s: %w", serviceID, err)
	}
	return count, nil
}

// ── PROJECT OPERATIONS ───────────────────────────────────────────────────────

// RegisterProject adds or updates a project in the registry.
func (s *Store) RegisterProject(p *Project) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO projects (id, name, path, language, project_type, config_json, registered_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name         = excluded.name,
			path         = excluded.path,
			language     = excluded.language,
			project_type = excluded.project_type,
			config_json  = excluded.config_json,
			updated_at   = excluded.updated_at
	`, p.ID, p.Name, p.Path, p.Language, p.ProjectType, p.ConfigJSON, now, now)
	if err != nil {
		return fmt.Errorf("register project %s: %w", p.ID, err)
	}
	return nil
}

// GetProject returns a single project by ID.
func (s *Store) GetProject(id string) (*Project, error) {
	row := s.db.QueryRow(
		`SELECT id, name, path, language, project_type, config_json, registered_at, updated_at
		 FROM projects WHERE id = ?`, id,
	)
	p := &Project{}
	err := row.Scan(&p.ID, &p.Name, &p.Path, &p.Language, &p.ProjectType, &p.ConfigJSON, &p.RegisteredAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get project %s: %w", id, err)
	}
	return p, nil
}

// GetAllProjects returns every registered project.
func (s *Store) GetAllProjects() ([]*Project, error) {
	rows, err := s.db.Query(
		`SELECT id, name, path, language, project_type, config_json, registered_at, updated_at
		 FROM projects ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("query all projects: %w", err)
	}
	defer rows.Close()

	var projects []*Project
	for rows.Next() {
		p := &Project{}
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &p.Language, &p.ProjectType, &p.ConfigJSON, &p.RegisteredAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// ── DOWNLOAD LOG OPERATIONS ──────────────────────────────────────────────────

// LogDownload records a file processed by Nexus Drop.
func (s *Store) LogDownload(d *DownloadLog) error {
	if d.DownloadedAt.IsZero() {
		d.DownloadedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO download_log (original_name, renamed_to, project, source, destination, method, action, confidence, downloaded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, d.OriginalName, d.RenamedTo, d.Project, d.Source, d.Destination, d.Method, d.Action, d.Confidence, d.DownloadedAt)
	if err != nil {
		return fmt.Errorf("log download: %w", err)
	}
	return nil
}

// GetRecentDownloads returns the N most recent download log entries.
func (s *Store) GetRecentDownloads(limit int) ([]*DownloadLog, error) {
	rows, err := s.db.Query(
		`SELECT id, original_name, renamed_to, project, source, destination, method, action, confidence, downloaded_at
		 FROM download_log ORDER BY downloaded_at DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent downloads: %w", err)
	}
	defer rows.Close()

	var logs []*DownloadLog
	for rows.Next() {
		d := &DownloadLog{}
		if err := rows.Scan(&d.ID, &d.OriginalName, &d.RenamedTo, &d.Project, &d.Source, &d.Destination, &d.Method, &d.Action, &d.Confidence, &d.DownloadedAt); err != nil {
			return nil, fmt.Errorf("scan download log: %w", err)
		}
		logs = append(logs, d)
	}
	return logs, rows.Err()
}

// ── MIGRATIONS ───────────────────────────────────────────────────────────────

func (s *Store) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS services (
			id             TEXT PRIMARY KEY,
			name           TEXT NOT NULL,
			project        TEXT NOT NULL DEFAULT '',
			desired_state  TEXT NOT NULL DEFAULT 'stopped',
			actual_state   TEXT NOT NULL DEFAULT 'unknown',
			provider       TEXT NOT NULL DEFAULT 'docker',
			config         TEXT NOT NULL DEFAULT '{}',
			fail_count     INTEGER NOT NULL DEFAULT 0,
			last_failed_at DATETIME,
			created_at     DATETIME NOT NULL,
			updated_at     DATETIME NOT NULL
		)`,

		// source  — which component emitted this event
		// trace_id — groups all events from a single operation
		`CREATE TABLE IF NOT EXISTS events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			service_id TEXT NOT NULL,
			type       TEXT NOT NULL,
			source     TEXT NOT NULL DEFAULT '',
			trace_id   TEXT NOT NULL DEFAULT '',
			payload    TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS health_logs (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			service_id TEXT NOT NULL,
			status     TEXT NOT NULL,
			exit_code  INTEGER NOT NULL DEFAULT 0,
			message    TEXT NOT NULL DEFAULT '',
			checked_at DATETIME NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS projects (
			id            TEXT PRIMARY KEY,
			name          TEXT NOT NULL,
			path          TEXT NOT NULL,
			language      TEXT NOT NULL DEFAULT '',
			project_type  TEXT NOT NULL DEFAULT '',
			config_json   TEXT NOT NULL DEFAULT '{}',
			registered_at DATETIME NOT NULL,
			updated_at    DATETIME NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS download_log (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			original_name TEXT NOT NULL,
			renamed_to    TEXT NOT NULL,
			project       TEXT NOT NULL DEFAULT '',
			source        TEXT NOT NULL DEFAULT '',
			destination   TEXT NOT NULL DEFAULT '',
			method        TEXT NOT NULL DEFAULT '',
			action        TEXT NOT NULL DEFAULT '',
			confidence    REAL NOT NULL DEFAULT 0.0,
			downloaded_at DATETIME NOT NULL
		)`,

		// Indexes
		`CREATE INDEX IF NOT EXISTS idx_services_project       ON services(project)`,
		`CREATE INDEX IF NOT EXISTS idx_services_desired_state ON services(desired_state)`,
		`CREATE INDEX IF NOT EXISTS idx_events_service_id      ON events(service_id)`,
		`CREATE INDEX IF NOT EXISTS idx_events_created_at      ON events(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_events_trace_id        ON events(trace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_events_source          ON events(source)`,
		`CREATE INDEX IF NOT EXISTS idx_health_service_id      ON health_logs(service_id)`,
		`CREATE INDEX IF NOT EXISTS idx_health_checked_at      ON health_logs(checked_at)`,
		`CREATE INDEX IF NOT EXISTS idx_download_project       ON download_log(project)`,
	}

	for _, migration := range migrations {
		if _, err := s.db.Exec(migration); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, migration)
		}
	}
	return nil
}

// ── SCAN HELPERS ─────────────────────────────────────────────────────────────

func scanService(row *sql.Row) (*Service, error) {
	svc := &Service{}
	err := row.Scan(
		&svc.ID, &svc.Name, &svc.Project,
		&svc.DesiredState, &svc.ActualState,
		&svc.Provider, &svc.Config,
		&svc.FailCount, &svc.LastFailedAt,
		&svc.CreatedAt, &svc.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan service: %w", err)
	}
	return svc, nil
}

func scanServices(rows *sql.Rows) ([]*Service, error) {
	var services []*Service
	for rows.Next() {
		svc := &Service{}
		err := rows.Scan(
			&svc.ID, &svc.Name, &svc.Project,
			&svc.DesiredState, &svc.ActualState,
			&svc.Provider, &svc.Config,
			&svc.FailCount, &svc.LastFailedAt,
			&svc.CreatedAt, &svc.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan service row: %w", err)
		}
		services = append(services, svc)
	}
	return services, rows.Err()
}
