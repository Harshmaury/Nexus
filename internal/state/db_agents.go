// @nexus-project: nexus
// @nexus-path: internal/state/db_agents.go
// Package state — agent registry persistence for Phase 14.
//
// An Agent is a remote engxa process running on another machine.
// It registers itself with the central engxd, receives desired state
// for its services, and reports actual state back via heartbeats.
//
// Schema (migration v4 — declared in db.go allMigrations):
//   agents table — one row per registered agent
//   id          TEXT PK  — stable machine identifier (hostname or user-set)
//   hostname    TEXT     — OS hostname of the agent machine
//   address     TEXT     — IP:port the agent reports itself at
//   token       TEXT     — shared secret for auth (set at agent init time)
//   last_seen   DATETIME — updated on every heartbeat
//   registered_at DATETIME
//
// Agent status is derived, not stored:
//   online  — last_seen within agentOnlineThreshold (30s)
//   offline — last_seen older than threshold
//
// NX-Fix-06 / Phase 15: init() registration removed.
// Migration v4 is now declared inline in db.go allMigrations slice.
// This file contains only the Agent model and store methods.
package state

import (
	"database/sql"
	"fmt"
	"time"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const agentOnlineThreshold = 30 * time.Second

// ── MODEL ─────────────────────────────────────────────────────────────────────

// Agent is a remote engxa process registered with this engxd.
type Agent struct {
	ID           string
	Hostname     string
	Address      string    // IP:port the agent listens on
	Token        string    // shared secret — never logged
	LastSeen     time.Time // zero if never checked in
	RegisteredAt time.Time
	Online       bool // derived — not stored
}

// ── STORE METHODS ─────────────────────────────────────────────────────────────

// RegisterAgent upserts an agent registration.
// Called when engxa starts up and connects to this engxd.
func (s *Store) RegisterAgent(a *Agent) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO agents (id, hostname, address, token, last_seen, registered_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname      = excluded.hostname,
			address       = excluded.address,
			token         = excluded.token,
			last_seen     = excluded.last_seen,
			registered_at = excluded.registered_at
	`, a.ID, a.Hostname, a.Address, a.Token, now, now)
	if err != nil {
		return fmt.Errorf("register agent %s: %w", a.ID, err)
	}
	return nil
}

// HeartbeatAgent updates last_seen for an agent.
func (s *Store) HeartbeatAgent(agentID string) error {
	_, err := s.db.Exec(
		`UPDATE agents SET last_seen = ? WHERE id = ?`,
		time.Now().UTC(), agentID,
	)
	if err != nil {
		return fmt.Errorf("heartbeat agent %s: %w", agentID, err)
	}
	return nil
}

// GetAgent returns a single agent by ID, or nil if not found.
func (s *Store) GetAgent(id string) (*Agent, error) {
	row := s.db.QueryRow(
		`SELECT id, hostname, address, token, last_seen, registered_at
		 FROM agents WHERE id = ?`, id,
	)
	return scanAgent(row)
}

// GetAgentToken returns the stored token for an agent.
// Returns ("", false, nil) when the agent is not registered.
// NX-H-02: fetches only the token column to minimise data exposure.
func (s *Store) GetAgentToken(id string) (string, bool, error) {
	var token string
	err := s.db.QueryRow(`SELECT token FROM agents WHERE id = ?`, id).Scan(&token)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get agent token %s: %w", id, err)
	}
	return token, true, nil
}

// GetAllAgents returns every registered agent with derived online status.
func (s *Store) GetAllAgents() ([]*Agent, error) {
	rows, err := s.db.Query(
		`SELECT id, hostname, address, token, last_seen, registered_at
		 FROM agents ORDER BY registered_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("get all agents: %w", err)
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		a, err := scanAgentRow(rows)
		if err != nil {
			return nil, err
		}
		a.Online = time.Since(a.LastSeen) <= agentOnlineThreshold
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// ── SCAN HELPERS ──────────────────────────────────────────────────────────────

func scanAgent(row *sql.Row) (*Agent, error) {
	a := &Agent{}
	var lastSeen sql.NullTime
	err := row.Scan(&a.ID, &a.Hostname, &a.Address, &a.Token, &lastSeen, &a.RegisteredAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan agent: %w", err)
	}
	if lastSeen.Valid {
		a.LastSeen = lastSeen.Time
		a.Online = time.Since(a.LastSeen) <= agentOnlineThreshold
	}
	return a, nil
}

func scanAgentRow(rows *sql.Rows) (*Agent, error) {
	a := &Agent{}
	var lastSeen sql.NullTime
	err := rows.Scan(&a.ID, &a.Hostname, &a.Address, &a.Token, &lastSeen, &a.RegisteredAt)
	if err != nil {
		return nil, fmt.Errorf("scan agent row: %w", err)
	}
	if lastSeen.Valid {
		a.LastSeen = lastSeen.Time
	}
	return a, nil
}
