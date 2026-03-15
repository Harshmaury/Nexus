# WORKFLOW-SESSION.md
# @version: 2.10.0
# @updated: 2026-03-16
# @repo: https://github.com/Harshmaury/Nexus

---

## HOW TO START A SESSION

```bash
cd ~/workspace/projects/apps/nexus && ./scripts/verify.sh
```

Paste the output block into Claude. Confirm + ask for task.

---

## SESSION KEY

Format: NX-<git-short-hash>-<YYYYMMDD>
Claude: fetch this file → match hash → confirm → ask for task.

---

## IDENTITY

Developer: Harsh Maury  |  GitHub: https://github.com/Harshmaury
Nexus: https://github.com/Harshmaury/Nexus
OS: Ubuntu 24.04 (WSL2) + Windows 11

---

## MACHINE

Go:1.24.1  Python:3.12.3  Node:22.22.0  .NET:10.0.103
Docker:28.2.2  kubectl:v1.35.1  Minikube:v1.38.1  Git:2.43.0

---

## BUILD STATUS
# Last verified: 2026-03-16

✅ Phases 1–14   Complete — full control plane on main
✅ ADR-002       Workspace observation implemented (2026-03-15)
  internal/eventbus/bus.go    5 workspace topics + 3 payloads
  internal/watcher/watcher.go WatchModeWorkspace, NewMulti(), workspace handler
  cmd/engxd/main.go           workspace watcher wired (NEXUS_WORKSPACE env var)

✅ NX-Fix-01     Debounce map data race eliminated (2026-03-16)
✅ NX-Fix-02     moveFile/copyFile extracted to pkg/osutil (2026-03-16)
  pkg/osutil/file.go                single MoveFile impl, io.CopyBuffer
  internal/intelligence/router.go   local funcs removed, uses osutil
  internal/daemon/server.go         local func removed, uses osutil
  internal/watcher/watcher.go debounceMap struct with sync.Mutex
                               AfterFunc callbacks serialised via Delete()
                               Verified clean with go build ./...

---

## WORKSPACE EVENT TOPICS (ADR-002)

Declared in internal/eventbus/bus.go — single source of truth.
All consumers import these constants. Never redefine locally.

  TopicWorkspaceFileCreated      "workspace.file.created"
  TopicWorkspaceFileModified     "workspace.file.modified"
  TopicWorkspaceFileDeleted      "workspace.file.deleted"
  TopicWorkspaceUpdated          "workspace.updated"
  TopicWorkspaceProjectDetected  "workspace.project.detected"

Consumer import:
  import "github.com/Harshmaury/Nexus/internal/eventbus"
  bus.Subscribe(eventbus.TopicWorkspaceFileCreated, handler)

---

## ENVIRONMENT VARIABLES

  NEXUS_DB_PATH             default ~/.nexus/nexus.db
  NEXUS_SOCKET              default /tmp/engx.sock
  NEXUS_HTTP_ADDR           default :8080
  NEXUS_DROP_DIR            default ~/nexus-drop
  NEXUS_WORKSPACE           default ~/workspace      ← new (ADR-002)
  NEXUS_RECONCILE_INTERVAL  default 5s
  NEXUS_HEALTH_INTERVAL     default 10s
  NEXUS_HEALTH_TIMEOUT      default 5s

---

## PLATFORM STATUS

Atlas Phase 1:  ready to start — event foundation now in place
Forge Phase 1:  waits for Atlas Phase 1 to complete

---

## ROADMAP

Nexus is feature-complete for the current platform needs.
Future Nexus work is driven by ADRs when new platform requirements emerge.

---

## CHANGELOG

2026-03-14  v2.0–v2.6  Phases 9–14, platform docs
2026-03-15  v2.8  fix: engine_test.go — deterministic partial failure test
2026-03-15  v2.7  ADR-002 impl — workspace event topics + watcher extension
2026-03-16  v2.9  fix: NX-Fix-01 — debounce map data race in watcher.go
2026-03-16  v2.10 fix: NX-Fix-02 — moveFile extracted to pkg/osutil, removed duplication
