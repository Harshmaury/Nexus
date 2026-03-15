# WORKFLOW-SESSION.md
# @version: 2.3.0
# @updated: 2026-03-15
# @repo: https://github.com/Harshmaury/Nexus

---

## HOW TO START A SESSION

```bash
cd ~/workspace/projects/apps/nexus && ./scripts/verify.sh
```

Paste the output block into Claude. Claude reads KEY + this file and is oriented.

---

## SESSION KEY

Format:  NX-<git-short-hash>-<YYYYMMDD>
Example: NX-2907c56-20260314

Claude protocol on receiving a key:
  1. Fetch this file from raw GitHub URL in the block
  2. Match commit hash to build status below
  3. Confirm: "Loaded NX-<hash>. Phase: <current>. Ready."
  4. Ask for task — never assume

---

## IDENTITY

Developer:  Harsh Maury
GitHub:     https://github.com/Harshmaury
Nexus:      https://github.com/Harshmaury/Nexus
OS:         Ubuntu 24.04 (WSL2) + Windows 11
Shell:      bash

---

## MACHINE
# Update only when a tool is installed or upgraded

Go:       1.24.1   → /usr/local/go
Python:   3.12.3   → WSL2 native
Node:     22.22.0  → WSL2
.NET:     10.0.103 → WSL2 + Windows
Docker:   28.2.2   → WSL2 engine
kubectl:  v1.35.1  → ~/bin/kubectl
Minikube: v1.38.1  → ~/bin/minikube
Git:      2.43.0   → WSL2

---

## BUILD STATUS
# Last verified: 2026-03-15

### ✅ Phase 1 — Daemon Core
### ✅ Phase 2 — Drop Intelligence
### ✅ Phase 3 — Bug Fixes (go.mod, Storer interface, notifier)
### ✅ Phase 7 — Internal Hardening
### ✅ Phase 8 — REST API (127.0.0.1, API key auth pending)
### ✅ Phase 9 — Runtime Providers (Process + K8s)
### ✅ Phase 10 — Drop Approve/Reject loop

### ✅ Phase 11 — Dependency Graph (2026-03-15)
  internal/state/db_deps.go    migration v3: depends_on column + index
                               GetServiceDependencies, SetServiceDependencies
  internal/state/storer.go     interface updated with dependency methods
  internal/daemon/engine.go    Kahn's topo sort before reconcile
                               deferred start if dependency not running
                               cycle detection with system alert + skip

---

## DEPENDENCY USAGE

In .nexus.yaml:
  services:
    - id: api
      depends_on: [db, cache]
    - id: db
    - id: cache

Start order:  db → cache → api  (topo sorted)
Stop order:   api → cache → db  (reverse topo)
Cycle:        system alert emitted, cyclic services skipped, rest unaffected

Reconciler behaviour on unmet dependency:
  → action = "deferred", retried next tick (every 5s by default)
  → no error, no crash — system converges naturally

---

## ROADMAP

Phase 12 — Observability (OTel spans, Prometheus counters, bubbletea TUI)
Phase 13 — Drop Intelligence v2 (ML layer 5, TF-IDF, engx drop train)
Phase 14 — Multi-machine agent mode (gRPC, remote state sync)

---

## CHANGELOG

2026-03-11  v1.0  Created
2026-03-13  v1.2  Phase 7+8 complete, 42 tests
2026-03-14  v2.0  Paths corrected, split into 3 files, 9 bugs fixed
2026-03-14  v2.1  Phase 9 — Process + K8s providers
2026-03-14  v2.2  Phase 10 — Drop approve/reject loop complete
2026-03-15  v2.3  Phase 11 — Dependency graph, topo sort, cycle detection
