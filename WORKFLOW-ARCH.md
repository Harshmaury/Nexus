# WORKFLOW-ARCH.md
# Architecture reference and AI coding rules for Nexus
# @version: 2.0.0
# @updated: 2026-03-14

---

## LAYER MAP

```
cmd/engx            →  Interface      CLI (cobra)
daemon/server.go    →  Transport      Unix socket + JSON protocol
api/                →  Transport      HTTP/JSON (127.0.0.1 only)
controllers/        →  Application    ProjectController, HealthController, RecoveryController
daemon/engine.go    →  Domain         Reconciler — desired vs actual state loop
state/              →  Domain         Store, Storer interface, Event, Service, Project
eventbus/           →  Messaging      Bus — all cross-component communication
intelligence/       →  Service        Detector, Renamer, Router, Pipeline, Notifier
pkg/runtime/        →  Execution      Provider interface → Docker, Process, K8s
watcher/            →  Infrastructure inotify filesystem watcher
config/             →  Config         All policy constants, env helpers
```

---

## DESIGN RULES
# Violating these breaks the architecture. No exceptions.

1. Nexus is NEVER coupled to UMS or any specific project.
   Projects register via .nexus.yaml — Nexus knows nothing about their internals.

2. Health controller is purely observational.
   It polls and emits events. It NEVER writes desired_state or actual_state.
   The reconciler is the sole owner of actual_state.

3. Recovery controller owns ALL restart policy.
   Nothing else decides when to restart. Thresholds live in config/policy.go only.

4. Reconciler is policy-free.
   It reads desired state and acts. It makes no policy decisions.

5. Every component communicates via the Event Bus.
   No direct cross-package calls between components. Import eventbus, not each other.

6. Interfaces over concrete types everywhere.
   state.Storer not *state.Store. runtime.Provider not *docker.Provider.
   This is non-negotiable — it enables testing and future replacement.

7. HTTP API binds to 127.0.0.1 only until auth is complete.
   Never 0.0.0.0. API key lives at ~/.nexus/api_key — never hardcoded.

8. Daemon is the single writer to SQLite.
   CLI is a pure socket client — it never opens the database directly.

---

## AI CODING RULES
# Apply to every file written for Nexus

BEFORE WRITING CODE:
  State understanding in 2 lines max
  List every file to create or modify
  Wait for approval

FILE NAMING:
  Format:  nexus_<package>_<filename>__<YYYYMMDD>_<HHMM>.go
  Example: nexus_runtime_process__20260314_1400.go
  Line 1:  // @nexus-project: nexus
  Line 2:  // @nexus-path: pkg/runtime/process/provider.go

CODE STANDARDS:
  SOLID — no exceptions
  Max 40 lines per function
  All errors handled explicitly — never swallow with _
  Named constants — no magic numbers anywhere
  Dependency injection — no package-level state
  Interfaces over concrete types
  No abbreviations in exported names

SECURITY:
  Never log secrets or env values
  Parameterized SQL queries only — never string concat
  Validate all inputs at package boundaries
  API key from file — never from env, never hardcoded

TESTING:
  Every new component gets a test file
  Mock state.Storer — never use a real SQLite DB in tests
  Table-driven tests where there are multiple cases
  Test file lives next to the file it tests

---

## EVENT BUS TOPICS

```
service.started          engine → loggers
service.stopped          engine → loggers
service.crashed          health → recovery (PublishAsync)
service.healed           health → loggers
service.state_changed    controllers → bus
service.health_check     health → loggers
service.recovery_needed  health → recovery (PublishAsync)
system.alert             any → loggers
drop.file_detected       watcher → pipeline
drop.file_routed         router → loggers
drop.file_quarantined    router → loggers
drop.pending_approval    router → CLI (via socket)
```

PublishAsync is mandatory for: service.crashed, service.recovery_needed
Reason: recovery handler does store reads/writes — blocking the health loop causes starvation.
