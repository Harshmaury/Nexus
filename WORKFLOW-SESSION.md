# WORKFLOW-SESSION.md
# @version: 2.1.0
# @updated: 2026-03-14
# @repo: https://github.com/Harshmaury/Nexus

---

## HOW TO START A SESSION

```bash
cd ~/workspace/projects/apps/nexus && ./scripts/verify.sh
```

Paste the output block into Claude. Claude reads KEY + this file and is oriented.
No re-explanation. No wasted tokens.

---

## SESSION KEY

Format:  NX-<git-short-hash>-<YYYYMMDD>
Example: NX-885a15d-20260314

Encodes:
  NX       → Nexus project
  885a15d  → exact commit (7 chars)
  20260314 → session date

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
# Update after completing each phase component
# Last verified: 2026-03-14 — branch: phase8-api — 9 bugs fixed, build clean

### ✅ Phase 1 — Daemon Core
  state/db.go               SQLite store + versioned migrations
  eventbus/bus.go           In-process pub/sub, PublishAsync, deadlock-safe
  pkg/runtime/provider.go   Provider interface
  daemon/engine.go          Reconciler loop (desired vs actual)
  controllers/health.go     Health polling — observational only
  controllers/recovery.go   Restart policy + persisted back-off
  daemon/server.go          Unix socket server, JSON protocol
  config/policy.go          Single source for all policy constants
  config/env.go             EnvOrDefault, ExpandHome helpers
  cmd/engxd/main.go         Daemon wiring + WaitGroup shutdown
  cmd/engx/main.go          CLI — register, project, services, events

### ✅ Phase 2 — Drop Intelligence
  watcher/watcher.go        inotify file watcher, debounce
  intelligence/detector.go  4-layer weighted confidence scoring
  intelligence/renamer.go   Canonical filename convention
  intelligence/router.go    Confidence router — non-blocking (bus-based approval)
  intelligence/notifier.go  Notifier interface — LinuxNotifier + NullNotifier
  intelligence/logger.go    Download audit log
  intelligence/pipeline.go  Full pipeline coordinator

### ✅ Phase 3 — Bug Fixes (2026-03-14)
  9 pre-build bugs resolved — go.mod version, Storer interface, notifier WSL fix

### ✅ Phase 7 — Internal Hardening
  Versioned migrations, persisted back-off, async crash events,
  WaitGroup shutdown, non-blocking router, policy dedup

### ✅ Phase 8 — REST API
  api/server.go             HTTP server + graceful shutdown (config.ShutdownTimeout)
  api/handler/projects.go   GET/POST projects
  api/handler/services.go   GET services
  api/handler/events.go     GET events
  api/middleware/           Logging + panic recovery
  Binds: 127.0.0.1 — API key auth pending

### ✅ Phase 9 — Runtime Providers (2026-03-14)
  pkg/runtime/process/provider.go  os/exec, PID files, SIGTERM→SIGKILL
  pkg/runtime/k8s/provider.go      kubectl binary, scale-to-0 stop
  cmd/engxd/main.go                Process + K8s providers wired at startup

---

## ROADMAP

Phase 10 — Drop approve/reject CLI (engx drop approve/reject — socket commands)
Phase 11 — Project dependency graph (depends_on in .nexus.yaml, ordered startup)
Phase 12 — Observability (OTel spans, Prometheus counters, bubbletea TUI)
Phase 13 — Drop Intelligence v2 (ML layer 5, TF-IDF, engx drop train)
Phase 14 — Multi-machine agent mode (gRPC, remote state sync)

---

## CHANGELOG

2026-03-11  v1.0  Created — workspace at ~/dev/nexus (old)
2026-03-13  v1.2  Phase 7+8 complete, 42 tests
2026-03-14  v2.0  Paths corrected to ~/workspace, split into 3 files, 9 bugs fixed
2026-03-14  v2.1  Phase 9 complete — Process + K8s providers, engxd wired
