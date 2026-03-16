// @nexus-project: nexus
// @nexus-path: internal/state/migrations.go
// Phase 15 migration: enrich events table for observation layer.
//
// v5 adds two columns to events:
//   component  TEXT — which platform domain emitted the event
//              (nexus | atlas | forge | drop | system)
//   outcome    TEXT — result of the action (success | failure | deferred | "")
//
// These fields make GET /events useful as an observation surface for
// future observer services (Navigator, Metrics) without a separate bus.
//
// The file was empty before Phase 15. allMigrations lives in db.go;
// this file appends v5 entries via an init() — NO. We follow NX-Fix-06:
// v5 is added directly to allMigrations in db.go. This file documents
// the rationale only and is not a Go source file.
//
// IMPORTANT: Do not add init() here. Edit allMigrations in db.go only.
package state
