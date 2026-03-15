# WORKFLOW-SESSION.md
# @version: 2.4.0
# @updated: 2026-03-15
# @repo: https://github.com/Harshmaury/Nexus

---

## HOW TO START A SESSION

```bash
cd ~/workspace/projects/apps/nexus && ./scripts/verify.sh
```

Paste the output block into Claude. Confirm + ask for task.

---

## SESSION KEY

Format:  NX-<git-short-hash>-<YYYYMMDD>
Claude: fetch this file → match hash → confirm → ask for task.

---

## IDENTITY

Developer:  Harsh Maury  |  GitHub: https://github.com/Harshmaury
Nexus:      https://github.com/Harshmaury/Nexus
OS:         Ubuntu 24.04 (WSL2) + Windows 11

---

## MACHINE
# Update only when a tool is installed or upgraded

Go:1.24.1  Python:3.12.3  Node:22.22.0  .NET:10.0.103
Docker:28.2.2  kubectl:v1.35.1  Minikube:v1.38.1  Git:2.43.0

---

## BUILD STATUS
# Last verified: 2026-03-15

✅ Phase 1   Daemon core (store, bus, engine, controllers, socket, CLI)
✅ Phase 2   Drop intelligence (watcher, detector, renamer, router, pipeline)
✅ Phase 3   Bug fixes (go.mod, Storer interface, notifier WSL fix)
✅ Phase 7   Internal hardening (migrations, back-off, async events, WaitGroup)
✅ Phase 8   REST API (127.0.0.1:8080, key auth pending)
✅ Phase 9   Runtime providers (Process PID-file, K8s kubectl scale-to-0)
✅ Phase 10  Drop approve/reject (pendingApprovals map, engx drop commands)
✅ Phase 11  Dependency graph (depends_on column, Kahn topo sort, deferred start)

✅ Phase 12  Observability (2026-03-15)
  internal/telemetry/metrics.go   atomic counters + gauges, zero new deps
  internal/daemon/engine.go       metrics wired — per-action counters, gauges
  internal/api/server.go          GET /metrics → JSON snapshot
  cmd/engxd/main.go               telemetry.New() wired into engine + API
  cmd/engx/main.go                engx watch — live terminal dashboard (2s poll)

---

## METRICS ENDPOINT

  curl http://127.0.0.1:8080/metrics

  {
    "uptime_seconds": 142.3,
    "reconcile_cycles_total": 28,
    "services_started_total": 4,
    "services_stopped_total": 1,
    "services_crashed_total": 0,
    "services_deferred_total": 2,
    "reconcile_errors_total": 0,
    "services_running": 4,
    "services_in_maintenance": 0
  }

## WATCH COMMAND

  engx watch              (refreshes every 2s)
  engx watch -i 5s        (custom interval)

  Shows: all projects, per-service desired/actual/health, fail count, summary bar.
  Symbols: ● running  ✗ crashed  ⚠ maintenance  ↻ recovering  ○ stopped

---

## ROADMAP

Phase 13 — Drop Intelligence v2 (ML classifier, TF-IDF, engx drop train)
Phase 14 — Multi-machine agent mode (gRPC, remote state sync)

---

## CHANGELOG

2026-03-11  v1.0  Created
2026-03-14  v2.0  Paths corrected, 3-file split, 9 bugs fixed
2026-03-14  v2.1  Phase 9 — providers
2026-03-14  v2.2  Phase 10 — drop approve/reject
2026-03-15  v2.3  Phase 11 — dependency graph
2026-03-15  v2.4  Phase 12 — metrics + engx watch
