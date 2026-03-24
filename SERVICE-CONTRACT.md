// @nexus-project: nexus
// @nexus-path: SERVICE-CONTRACT.md
# SERVICE-CONTRACT.md — Nexus
# @version: 1.7.3
# @updated: 2026-03-25

**Port:** 8080 · **DB:** `~/.nexus/nexus.db` · **Domain:** Control

---

## Code

```
cmd/engxd/main.go              startup, goroutine lifecycle, signal handling
cmd/engx/                      CLI subcommands (one file per command group)
cmd/engxa/main.go              remote agent heartbeat + sync loop
internal/daemon/reconciler.go  5s reconcile tick — desired→actual state
internal/controllers/          ProjectController, HealthController, RecoveryController
internal/state/db.go           Storer interface, SQLite, versioned migrations
internal/eventbus/bus.go       in-process pub/sub, all topic constants
internal/api/                  HTTP :8080, response envelope {ok, data, error}
internal/watcher/              fsnotify — WatchModeDropFolder + WatchModeWorkspace
pkg/runtime/                   process / docker / k8s providers
```

---

## Contract

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/health` | none | `{"ok":true,"status":"healthy","service":"nexus"}` |
| GET | `/projects` | token | All registered projects |
| POST | `/projects/register` | token | Register project — `ProjectRegisterRequest` |
| POST | `/projects/:id/start` | token | Queue service starts (Forge only) |
| POST | `/projects/:id/stop` | token | Queue service stops (Forge only) |
| GET | `/services` | token | All managed services |
| POST | `/services/register` | token | Register service — `ServiceRegisterRequest` |
| GET | `/agents` | token | Registered remote agents |
| GET | `/events` | token | `?since=<id>&limit=<n>` — cursor-based event log |
| GET | `/events/stream` | token | SSE fan-out stream |
| GET | `/metrics` | token | Runtime counters — `NexusMetricsDTO` |
| GET | `/system/graph` | token | Unified service topology |
| POST | `/system/validate` | token | Pre-execution policy gate |

All responses: `accord.Response[T]` — `{"ok": bool, "data": T, "error": "string"}`.

### Event schema

`EventDTO` fields: `id`, `service_id`, `type`, `source`, `trace_id`, `span_id`, `parent_span_id`, `level`, `component`, `outcome`, `payload`, `actor`, `created_at`.

Payload type registry: `Accord/api/payload.go → EventPayloadType`.

### Versioning

`X-Nexus-API-Version: 1` on all responses. Breaking changes require ADR + major bump.

### Failure conditions

| Code | Condition |
|------|-----------|
| 401 | Missing or invalid `X-Service-Token` |
| 404 | Project or service not found |
| 409 | Project or service already registered |
| 500 | Unexpected internal error |

---

## Control

**Reconciler:** ticks every 5s. Reads `desired_state` per service → instructs runtime provider → writes `actual_state`. Single writer to SQLite.

**Crash policy:** `fail_count ≥ threshold` (from `internal/config/policy.go`) → sets `desired_state = maintenance`. Recovery controller polls maintenance projects every 30s.

**Event log:** append-only. `GET /events?since=<id>` is a stable cursor — never resets. SSE broker evicts slow clients after 5s send timeout.

**Token enforcement:** `X-Service-Token` validated in `internal/api/middleware/service_auth.go`. `ENGX_AUTH_REQUIRED=true` → missing token = `FATAL` at startup.

---

## Context

- Sole project registry and filesystem observer on the platform. No other service writes project state.
- All other services discover Nexus at `Canon/identity.DefaultNexusAddr` or env override.
- Atlas, Forge, and all observers subscribe by polling `GET /events?since=<id>`.
- Nexus has no upstream platform dependencies. It depends only on local filesystem and SQLite.
