# NEXUS MASTER WORKFLOW
# @version: 1.0.0
# @updated: 2026-03-12
# @author: Harsh Maury
# @repo: https://github.com/Harshmaury/Nexus
#
# HOW TO UPDATE THIS FILE:
# Only edit the section that changed. Each section is independent.
# After any change: git add WORKFLOW.md && git commit -m "workflow: update [SECTION_NAME]"
#
# HOW TO USE WITH AI:
# Paste this raw link at start of every chat:
# https://raw.githubusercontent.com/Harshmaury/Nexus/main/WORKFLOW.md

---

## [IDENTITY]
Developer:    Harsh Maury
Role:         CS Engineering Student
GitHub:       https://github.com/Harshmaury
Nexus Repo:   https://github.com/Harshmaury/Nexus
UMS Repo:     https://github.com/Harshmaury/AspireApp1
OS:           Ubuntu 24.04.4 LTS (WSL2) + Windows 11
Shell:        bash | ~/dev/nexus is home base

---

## [MACHINE]
# UPDATE THIS SECTION: only when you install/upgrade a tool
Go:        1.24.1  → /usr/local/go
Python:    3.12.3  → WSL2 native
Node:      22.22.0 → WSL2 npm-global
.NET:      10.0.103 → WSL2 + Windows
Docker:    28.2.2  → WSL2 engine
kubectl:   v1.35.1 → ~/bin/kubectl
Minikube:  v1.38.1 → ~/bin/minikube
Git:       2.43.0  → WSL2

---

## [STRUCTURE]
# UPDATE THIS SECTION: only when you add/remove a folder
~/
├── bin/                    → kubectl, minikube, engx binaries
├── dev/
│   ├── nexus/              → Nexus engine (Go) — THIS REPO
│   ├── projects/
│   │   └── ums/            → UMS .NET 10 microservices
│   ├── experiments/
│   │   └── ai/             → AI Agent platform (Python+Docker)
│   ├── learning/           → tutorials, practice
│   ├── tools/
│   │   ├── debuggers/vsdbg
│   │   └── runners/actions-runner
│   └── archive/            → old/inactive projects
└── go/                     → Go workspace (auto-managed)

---

## [PROJECTS]
# UPDATE THIS SECTION: when you add a new project or change stack

### nexus
path:     ~/dev/nexus
language: Go 1.24
type:     platform-daemon
purpose:  Controls ALL other projects. Never coupled to them.
github:   https://github.com/Harshmaury/Nexus
status:   IN DEVELOPMENT — Phase 1 complete, Phase 2 complete, refactor done
start:    go run ./cmd/engxd/
build:    go build -o ~/bin/engxd ./cmd/engxd/
test:     go test ./...

### ums
path:     /mnt/c/Users/harsh/source/repos/AspireApp1
language: .NET 10, C#
type:     microservices (9 services)
purpose:  University Management System
github:   https://github.com/Harshmaury/AspireApp1
infra:    Minikube (K8s), Kafka, PostgreSQL, YARP gateway
services: identity:5002 academic:5004 student:5003 attendance:5005
          examination:5006 fee:5007 faculty:5008 hostel:5009 notification:5010
observability: Grafana:3000 Prometheus:9090 Jaeger:16686 Seq:8081
status:   PRODUCTION — running on Minikube

### ai
path:     ~/dev/experiments/ai
language: Python 3.12
type:     ai-agent
purpose:  Multi-agent AI platform
infra:    Docker Compose
ports:    8000 (API), 5900 (VNC)
status:   RUNNING — container: ai-ai_bridge-1
warning:  Do not stop without saving state

---

## [NEXUS_BUILD_STATUS]
# UPDATE THIS SECTION: after completing each phase component

Phase 1 — Daemon Core:
  [x] 01 internal/state/db.go              → SQLite state store ✅ DONE
  [x] 02 internal/eventbus/bus.go          → Event pub/sub ✅ DONE
  [x] 03 pkg/runtime/provider.go           → Provider interface ✅ DONE
  [ ] 03 pkg/runtime/docker/provider.go    → Docker provider (NEXT)
  [ ] 03 pkg/runtime/k8s/provider.go       → K8s provider
  [ ] 03 pkg/runtime/process/provider.go   → Process provider
  [x] 04 internal/daemon/engine.go         → Reconciler loop ✅ DONE
  [x] 05 internal/controllers/health.go    → Health controller ✅ DONE
  [x] 06 internal/controllers/recovery.go  → Recovery + back-off ✅ DONE
  [x] 07 internal/daemon/server.go         → Unix socket server ✅ DONE
  [x] 08 cmd/engxd/main.go                 → Daemon entry point ✅ DONE
  [x] 08 cmd/engx/main.go                  → CLI entry point ✅ DONE

Phase 1 — Refactor (complete 2026-03-12):
  [x] internal/config/policy.go            → Single source for all policy constants ✅
  [x] internal/config/env.go               → Centralised env helpers, expandHome ✅
  [x] cmd/engxd/main.go                    → sync.WaitGroup shutdown, log/slog ✅
  [x] internal/controllers/recovery.go     → context.Context Run() signature ✅
  [x] internal/controllers/project_controller.go → imports config, no local constants ✅
  [x] internal/daemon/engine.go            → imports config, slog, logged errors ✅
  [x] cmd/engx/main.go                     → imports config, no local duplicates ✅

Phase 2 — Drop Intelligence:
  [x] internal/watcher/watcher.go          → inotify file watcher ✅ DONE
  [x] internal/intelligence/detector.go    → 4-layer detection ✅ DONE
  [x] internal/intelligence/renamer.go     → Smart rename ✅ DONE
  [x] internal/intelligence/router.go      → Confidence router ✅ DONE
  [x] internal/intelligence/logger.go      → Download audit log ✅ DONE
  [x] internal/intelligence/pipeline.go    → Full pipeline coordinator ✅ DONE

Phase 3 — CLI Commands:
  [ ] engx start / stop / status / logs / deploy / doctor

Phase 4 — Providers:
  [ ] pkg/runtime/docker/provider.go       → Docker SDK implementation
  [ ] pkg/runtime/k8s/provider.go          → client-go implementation
  [ ] pkg/runtime/process/provider.go      → os/exec implementation

Phase 5 — Plugin System:
  [ ] plugins/docker/
  [ ] plugins/kubernetes/
  [ ] plugins/health/

---

## [AI_RULES]
# NEVER UPDATE THIS SECTION — it is permanent

You are helping Harsh Maury build Nexus — a Go daemon that acts
as a local developer control plane for all his projects.

BEFORE WRITING ANY CODE:
1. State what you understood in 2 lines max
2. Ask UP TO 3 clarifying questions if unclear
3. List every file you will create or modify
4. Wait for approval — then code

FILE NAMING (MANDATORY):
  Format:  [project]__[feature]__[YYYYMMDD_HHMM].[ext]
  Example: nexus__eventbus_core__20260311_1500.go
  Line 1:  // @nexus-project: nexus
  Line 2:  // @nexus-path: internal/eventbus/bus.go

CODE STANDARDS (MANDATORY):
  - SOLID principles — no exceptions
  - Max 40 lines per function
  - All errors handled explicitly — never swallow
  - Named constants — no magic numbers
  - Dependency injection everywhere
  - Interfaces over concrete types
  - No abbreviations in names

ARCHITECTURE RULES:
  - Nexus is NEVER directly coupled to UMS or any project
  - Projects register with Nexus via .nexus.yaml manifest
  - Clean Architecture: Domain → Application → Infrastructure
  - Every component communicates via the Event Bus only
  - All policy constants live in internal/config — never redeclare locally

SECURITY:
  - Never log secrets or env values
  - Parameterized queries only
  - Validate all inputs at boundaries

DROP WORKFLOW:
  - AI produces files as nexus__[feature]__[YYYYMMDD_HHMM].go
  - Harsh downloads to C:\Users\harsh\Downloads\nexus-drop\
  - Harsh runs cp commands to place files + go build ./... to verify
  - On success: git add . && git commit && git push

---

## [CONTEXT_DUMP]
# UPDATE THIS SECTION: only if you move the script or change projects

Script:   ~/dev/nexus/scripts/context-dump.sh
Drops to: ~/bin/nexus-drops/

Commands:
  context-dump nexus "your task"    → Nexus project only
  context-dump ums "your task"      → UMS project only
  context-dump ai "your task"       → AI project only
  context-dump all "your task"      → all projects

Drop folder (Chrome downloads here):
  Windows: C:\Users\harsh\Downloads\nexus-drop\
  WSL2:    /mnt/c/Users/harsh/Downloads/nexus-drop/

---

## [GIT_WORKFLOW]
# UPDATE THIS SECTION: only if you change your branching strategy

Nexus repo:
  main branch → always stable, always buildable
  feature branches → feature/[component-name]
  commit format → type: description
    types: feat | fix | docs | refactor | test | chore

One-liner commit (after each component):
  git add . && git commit -m "feat: [what you built]" && git push origin main

UMS repo:
  master branch → production
  CI/CD → GitHub Actions (self-hosted WSL2 runner)
  runner path → ~/dev/tools/runners/actions-runner

---

## [HOW_TO_INTEGRATE_NEW_PROJECT]
# UPDATE THIS SECTION: paste new project entry under [PROJECTS] above

Steps (3 minutes):
  1. mkdir -p ~/dev/projects/new-project
  2. Create .nexus.yaml in project root (see template below)
  3. engx register ~/dev/projects/new-project
  4. Add one line to context-dump.sh PROJECTS array
  5. Add entry under [PROJECTS] section above
  6. git add WORKFLOW.md && git commit -m "workflow: add new-project"

.nexus.yaml template:
  name: my-project
  type: web-api
  language: go
  version: 1.0.0
  paths:
    root: ~/dev/projects/my-project
    src: src/
    tests: tests/
  commands:
    start: go run ./cmd/server/
    build: go build ./...
    test:  go test ./...
  ports:
    - 8090: api
  keywords:
    - "package main"
    - "my-project"

---

## [CHANGELOG]
# UPDATE THIS SECTION: every time you make a significant change
# Format: YYYY-MM-DD | [SECTION] | what changed

2026-03-11 | [IDENTITY]  | Created WORKFLOW.md
2026-03-11 | [MACHINE]   | Go 1.24.1, Python 3.12.3, Node 22, .NET 10
2026-03-11 | [STRUCTURE] | Cleaned home root, organized ~/dev/
2026-03-11 | [PROJECTS]  | Registered nexus, ums, ai
2026-03-11 | [BUILD]     | Phase 1 started — state store complete
2026-03-11 | [BUILD]     | Phase 1 complete — all daemon core components done
2026-03-11 | [BUILD]     | Phase 2 complete — Drop Intelligence pipeline done
2026-03-12 | [BUILD]     | Refactor complete — config package, WaitGroup shutdown, slog, dedup
2026-03-12 | [AI_RULES]  | Added DROP WORKFLOW section documenting the drop file process
