// @nexus-project: nexus
// @nexus-path: internal/config/policy.go
// Package config holds all tunable policy constants for the Nexus daemon.
// Every component that makes decisions about failure thresholds, back-off
// schedules, or maintenance windows imports from here — never defines its own.
//
// Before this file existed, the value 3 (failure threshold) and 60 (window)
// were independently declared in three separate packages:
//   - internal/daemon/engine.go     → maxFailuresBeforeMaintenance = 3
//   - internal/controllers/recovery.go → maintenanceFailureThreshold = 3
//   - internal/controllers/project_controller.go → maxRecentFailuresThreshold = 3
//
// Changing the threshold in one would silently leave the others stale.
// Now there is exactly one place to change policy.
package config

import "time"

// ── FAILURE & RECOVERY POLICY ─────────────────────────────────────────────────

// MaintenanceFailureThreshold is the number of crashes within
// MaintenanceWindowMinutes that triggers maintenance mode.
// Used by: engine (reconciler), recovery controller, project controller.
const MaintenanceFailureThreshold = 3

// MaintenanceWindowMinutes is the rolling window for counting recent failures.
const MaintenanceWindowMinutes = 60

// ── BACK-OFF SCHEDULE ────────────────────────────────────────────────────────

// BackOffSchedule maps fail count (0-indexed) to the delay before
// the next restart attempt. Beyond the last entry → maintenance mode.
//
//	Attempt 1 (fail_count=0) →  5s
//	Attempt 2 (fail_count=1) → 15s
//	Attempt 3 (fail_count=2) → 30s
//	Beyond schedule          → maintenance
var BackOffSchedule = []time.Duration{
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
}

// ── RECONCILER DEFAULTS ───────────────────────────────────────────────────────

// DefaultReconcileInterval is how often the reconciler runs if not overridden
// by the NEXUS_RECONCILE_INTERVAL environment variable.
const DefaultReconcileInterval = 5 * time.Second

// ── HEALTH CHECK DEFAULTS ────────────────────────────────────────────────────

// DefaultHealthInterval is how often the health controller polls services.
const DefaultHealthInterval = 10 * time.Second

// DefaultHealthTimeout is the per-check deadline.
const DefaultHealthTimeout = 5 * time.Second

// ── SHUTDOWN ─────────────────────────────────────────────────────────────────

// ShutdownTimeout is the maximum time the daemon waits for all components
// to stop cleanly before forcing exit.
const ShutdownTimeout = 10 * time.Second
