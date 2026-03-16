# WORKFLOW-SESSION.md
# Session: NX-phase15-event-enrichment
# Date: 2026-03-17

## What changed

### Nexus Phase 15 — Event log enrichment + X-Trace-ID propagation

**Goal:** Make GET /events a rich observation surface for future observer
services without adding new infrastructure.

### Files changed

#### New files
- `nexus/internal/api/middleware/traceid.go`
  X-Trace-ID middleware. Generates trace ID if absent, stores in context,
  echoes in response header.

#### Modified — Nexus
- `nexus/internal/state/db.go`
  Migration v5: adds `component` and `outcome` columns to events table.
  Updated AppendEvent signature. New GetEventsSince(sinceID, limit).
  Updated GetRecentEvents + GetEventsByTrace to scan new fields.

- `nexus/internal/state/storer.go`
  AppendEvent interface updated with component + outcome params.
  GetEventsSince added to interface.

- `nexus/internal/state/events.go`
  EventWriter updated: component field, outcome on every write method.
  New constants: OutcomeSuccess/Failure/Deferred/Info, ComponentNexus/Drop/System.
  New method: ServiceDeferred (for reconciler deferred starts).
  NewEventWriter now takes component as third arg.

- `nexus/internal/api/handler/events.go`
  Added ?since=<id> query param support (incremental polling for Atlas).
  Refactored into listSince / listByTrace / listRecent private methods.

- `nexus/internal/api/server.go`
  middleware.TraceID wired into handler chain (after ServiceAuth).

- `nexus/pkg/events/topics.go`
  TraceIDHeader = "X-Trace-ID" exported constant.

- `nexus/internal/controllers/health.go`
  NewEventWriter call updated with ComponentNexus.

- `nexus/internal/controllers/project_controller.go`
  NewEventWriter call updated with ComponentNexus.

- `nexus/internal/controllers/recovery.go`
  NewEventWriter call updated with ComponentNexus.

- `nexus/internal/daemon/engine.go`
  NewEventWriter call updated with ComponentNexus.

- `nexus/internal/watcher/watcher.go`
  NewEventWriter calls updated with ComponentDrop.

- `nexus/internal/intelligence/pipeline.go`
  NewEventWriter call updated with ComponentDrop.

- `nexus/internal/intelligence/router.go`
  NewEventWriter call updated with ComponentDrop.

#### Modified — Atlas
- `atlas/internal/nexus/client.go`
  get() now propagates X-Trace-ID from context (Phase 15).

- `atlas/internal/nexus/subscriber.go`
  poll() updated to use ?since=<id> for efficient incremental polling.
  Removed unused strconv import.

## Apply command

```bash
cd ~/workspace/projects/apps/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-phase15-event-enrichment-20260317-0000.zip -d . && \
go build ./... && \
git add \
  internal/api/middleware/traceid.go \
  internal/api/handler/events.go \
  internal/api/server.go \
  internal/state/db.go \
  internal/state/events.go \
  internal/state/migrations.go \
  internal/state/storer.go \
  internal/controllers/health.go \
  internal/controllers/project_controller.go \
  internal/controllers/recovery.go \
  internal/daemon/engine.go \
  internal/intelligence/pipeline.go \
  internal/intelligence/router.go \
  internal/watcher/watcher.go \
  pkg/events/topics.go \
  WORKFLOW-SESSION.md && \
git commit -m "feat: phase 15 — event enrichment + X-Trace-ID propagation" && \
git push origin main

cd ~/workspace/projects/apps/atlas && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-phase15-event-enrichment-20260317-0000.zip -d . && \
go build ./... && \
git add \
  internal/nexus/client.go \
  internal/nexus/subscriber.go \
  WORKFLOW-SESSION.md && \
git commit -m "feat: phase 15 — X-Trace-ID propagation + since-based polling" && \
git push origin main
```

## Verification

```bash
# Confirm enriched events
curl -s http://127.0.0.1:8080/events | jq '.data[0] | {id, type, component, outcome, trace_id}'

# Confirm since polling works
curl -s "http://127.0.0.1:8080/events?since=0&limit=5" | jq '.data[] | {id, type}'

# Confirm trace ID in response header
curl -sv http://127.0.0.1:8080/events 2>&1 | grep X-Trace-ID

# Confirm Atlas still polls correctly
engx events -n 10
```
