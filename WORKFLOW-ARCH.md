# WORKFLOW-ARCH.md
# @version: 3.0.0
# @updated: 2026-03-16

---

## LAYER MAP

```
cmd/engx            CLI             cobra subcommands
cmd/engxd           Daemon          startup wiring, goroutine lifecycle
cmd/engxa           Remote agent    heartbeat + sync loop
internal/api/       HTTP            127.0.0.1:8080, response: {ok, data, error}
internal/daemon/    Core            reconciler engine, unix socket server
internal/controllers/  Application ProjectController, HealthController, RecoveryController
internal/state/     Storage         Storer interface, SQLite, versioned migrations
internal/eventbus/  Messaging       Bus — all cross-component communication
internal/intelligence/ Drop AI      Detector, Classifier, Router, Pipeline
internal/watcher/   Filesystem      inotify, WatchModeDropFolder + WatchModeWorkspace
pkg/runtime/        Providers       Docker, Process, K8s
pkg/osutil/         Shared utils    MoveFile (cross-device safe)
pkg/events/         Public API      workspace topic constants for Atlas + Forge
internal/config/    Config          env helpers, policy constants
```

---

## DESIGN RULES

1. Nexus is never coupled to UMS or any specific project.
2. Health controller is read-only — it never writes state.
3. Recovery controller owns all restart policy — thresholds in config/policy.go only.
4. Reconciler is policy-free — reads desired state and acts.
5. All cross-component communication goes through the event bus.
6. Interfaces over concrete types — state.Storer, runtime.Provider, not *Store.
7. HTTP API binds to 127.0.0.1 only.
8. Daemon is the single writer to SQLite — CLI never opens the DB.
9. All migrations live in internal/state/db.go — never in init() functions.
10. Token comparison uses crypto/subtle.ConstantTimeCompare.

---

## AI CODING RULES

BEFORE WRITING CODE:
  State understanding in 2 lines
  List every file to create or modify
  Grep all import usages before adding or removing any import
  Wait for approval

FILE NAMING:
  Format:  nexus_<package>_<filename>__<YYYYMMDD>_<HHMM>.go
  Line 1:  // @nexus-project: nexus
  Line 2:  // @nexus-path: <relative/path/to/file.go>

CODE STANDARDS:
  SOLID — no exceptions
  Max 40 lines per function
  All errors handled explicitly — never swallow with _
  Named constants — no magic numbers
  Dependency injection — no package-level mutable state
  Interfaces over concrete types

SECURITY:
  Never log secrets or env values
  Parameterised SQL only — never string concat
  Validate all inputs at package boundaries

TESTING:
  Every new component gets a test file
  Mock state.Storer — never use real SQLite in tests
  Table-driven tests for multiple cases

---

## DROP FOLDER

All deliveries go to: C:\Users\harsh\Downloads\engx-drop\
WSL2 path:            /mnt/c/Users/harsh/Downloads/engx-drop/

---

## EVENT BUS TOPICS

```
service.started          engine → loggers
service.stopped          engine → loggers
service.crashed          health → recovery   (PublishAsync — never block health loop)
service.healed           health → loggers
service.recovery_needed  health → recovery   (PublishAsync)
system.alert             any → loggers
drop.file_detected       watcher → pipeline
drop.file_routed         router → loggers
drop.pending_approval    router → CLI socket
workspace.file.created   watcher → Atlas/Forge subscribers
workspace.file.modified  watcher → Atlas/Forge subscribers
workspace.file.deleted   watcher → Atlas/Forge subscribers
workspace.updated        watcher → Atlas/Forge subscribers (debounced)
workspace.project.detected  watcher → Atlas/Forge subscribers
```
