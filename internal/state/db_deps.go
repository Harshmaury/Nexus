// @nexus-project: nexus
// @nexus-path: internal/state/db_deps.go
// Package state — dependency persistence for Phase 11.
//
// NX-Fix-06: init() removed — v3 migration consolidated into db.go.
// This file now contains only store methods for dependency persistence.
// The column stores a JSON array of service IDs e.g. ["db", "cache"].
//
// Why a column, not a junction table?
//   At Nexus scale (dozens of services, not thousands) a JSON column is
//   simpler, readable in SQLite Browser, and avoids a join on every
//   reconcile cycle. A junction table is the right choice at scale —
//   add a migration when needed.
package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ── STORE METHODS ─────────────────────────────────────────────────────────────
// NX-Fix-06: v3 migration (depends_on column) moved to db.go allMigrations.
// init() removed — ordering is now guaranteed by slice position in db.go.

// GetServiceDependencies returns the IDs of services that must be running
// before serviceID can be started. Returns nil slice if none declared.
func (s *Store) GetServiceDependencies(serviceID string) ([]string, error) {
	var raw string
	err := s.db.QueryRow(
		`SELECT depends_on FROM services WHERE id = ?`, serviceID,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get depends_on for %s: %w", serviceID, err)
	}

	var deps []string
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(raw), &deps); err != nil {
		return nil, fmt.Errorf("parse depends_on for %s: %w", serviceID, err)
	}
	return deps, nil
}

// SetServiceDependencies writes the depends_on list for a service.
// An empty or nil slice clears the dependencies.
func (s *Store) SetServiceDependencies(serviceID string, deps []string) error {
	if deps == nil {
		deps = []string{}
	}
	raw, err := json.Marshal(deps)
	if err != nil {
		return fmt.Errorf("marshal depends_on for %s: %w", serviceID, err)
	}
	_, err = s.db.Exec(
		`UPDATE services SET depends_on = ?, updated_at = ? WHERE id = ?`,
		string(raw), time.Now().UTC(), serviceID,
	)
	if err != nil {
		return fmt.Errorf("set depends_on for %s: %w", serviceID, err)
	}
	return nil
}
