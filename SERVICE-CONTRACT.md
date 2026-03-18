# SERVICE-CONTRACT.md — Nexus

**Service:** nexus
**Domain:** Control
**Port:** 8080
**ADRs:** ADR-001 (registry), ADR-002 (observation), ADR-003 (protocol),
         ADR-005 (lifecycle), ADR-008 (auth), ADR-015 (SSE)
**Version:** 1.2.0-phase16
**Updated:** 2026-03-18

---

## Role

Central control plane. Owns the canonical project registry, filesystem
observation, service runtime state, and the platform event bus. The most
authoritative service in the platform — all other services depend on it
directly or indirectly.

---

## Inputs

- Developer CLI commands via `engx` client
- Remote agent connections via `engxa`
- `POST /projects` — project registration
- `POST /projects/:id/start|stop` — lifecycle instructions (from Forge only)
- Filesystem events via `fsnotify` watcher (internal — no HTTP)
- Drop folder file events (internal — `internal/intelligence/` pipeline)

---

## Outputs

- `GET /health` — health check (no auth)
- `GET /projects` — project registry
- `GET /events?since=<id>` — structured event log (auth required)
- `GET /events/stream` — SSE fan-out stream (auth required, ADR-015)
- `GET /metrics` — runtime telemetry counters (JSON, no Prometheus dependency)
- `POST /projects/:id/start|stop` — lifecycle control (Forge only)

---

## Dependencies

None. Nexus has no upstream platform service dependencies.
It depends only on the local filesystem and SQLite.

---

## Guarantees

- Project registry is the single canonical source of truth for all projects.
  No other service maintains its own project list (ADR-001).
- Filesystem observation is owned exclusively by Nexus.
  No other service watches the filesystem (ADR-002).
- All inter-service calls carry `X-Service-Token`. `/health` exempt (ADR-008).
- Event log is append-only and queryable by `since=<id>` cursor.
- SSE broker evicts slow clients after 5s send timeout — does not block the event bus.
- `X-Trace-ID` is injected on every response by middleware.
- All migrations are in a single ordered slice in `internal/state/db.go`.

---

## Non-Responsibilities

- Nexus does not execute developer workflows — that is Forge's domain.
- Nexus does not index workspace source files — that is Atlas's domain.
- Nexus does not evaluate policy — that is Guardian's domain.
- Nexus does not call any observer service (Metrics, Navigator, Guardian,
  Observer, Sentinel). Data flows out of Nexus, never back in from observers.
- Nexus does not make AI calls of any kind.

---

## Data Authority

**Primary authority for:**
- Project registry — `~/.nexus/nexus.db` → `projects` table
- Service runtime state — desired and actual state per project
- Platform event log — `events` table, append-only
- Filesystem workspace events — published to all subscribers

---

## Concurrency Model

- SQLite store accessed through `state.Storer` interface.
- `EventWriter` notifies the SSE broker after each store write.
- SSE broker manages per-client goroutines with slow-client eviction.
- HTTP server is standard `net/http` with read/write timeouts.
- Recovery controller handles crash detection and restart logic.
