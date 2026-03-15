# WORKFLOW-WORKSPACE.md
# Workspace layout, projects, integration guide, git workflow
# @version: 3.0.0
# @updated: 2026-03-15

---

## WORKSPACE

```
~/
├── bin/                          engx, engxd, engxa, atlas, kubectl, minikube
├── actions-runner/               Self-hosted GitHub Actions runner (UMS CI/CD)
├── go/                           Go toolchain (auto-managed)
└── workspace/
    ├── AI_CONTEXT.md/            Platform constraints + AI rules (6 sections)
    ├── developer-platform/       Platform governance — ADRs, capability docs, no code
    ├── architecture/             Workspace-level architecture docs (Atlas indexes here)
    │   └── decisions/            ADR-001 through ADR-NNN
    ├── projects/
    │   ├── apps/                 Primary active platform projects
    │   │   ├── nexus/            ← THIS REPO — Control domain
    │   │   ├── atlas/            Knowledge domain (Phase 1 complete)
    │   │   └── forge/            Execution domain (Phase 1 not started)
    │   ├── Test/
    │   │   └── nexus-prototype/  Prototype reference — do not modify
    │   ├── ai/                   AI agent platform (future)
    │   ├── api/                  API services (future)
    │   ├── automation/           Automation scripts (future)
    │   ├── cli/                  CLI tools (future)
    │   └── archive/              Inactive / deprecated projects
    ├── experiments/
    ├── datasets/
    ├── models/
    ├── shared/
    └── templates/
```

### Drop Folders

  Purpose            Windows                                       WSL2
  ─────────────────────────────────────────────────────────────────────────────
  Nexus code         C:\Users\harsh\Downloads\nexus-drop\          /mnt/c/Users/harsh/Downloads/nexus-drop/
  Atlas code         C:\Users\harsh\Downloads\atlas-drop\          /mnt/c/Users/harsh/Downloads/atlas-drop/
  Forge code         C:\Users\harsh\Downloads\forge-drop\          /mnt/c/Users/harsh/Downloads/forge-drop/
  Workspace docs     C:\Users\harsh\Downloads\workdox-dump\        /mnt/c/Users/harsh/Downloads/workdox-dump/

---

## PROJECTS

### nexus
path:     ~/workspace/projects/apps/nexus
repo:     https://github.com/Harshmaury/Nexus
language: Go 1.24.1
type:     platform-daemon
domain:   Control
purpose:  Local developer control plane — orchestrates all platform services
status:   Phases 1–14 complete + ADR-002 (workspace events) — feature complete
run:      engxd &
build:    go build -o ~/bin/engxd ./cmd/engxd/ && \
          go build -o ~/bin/engx  ./cmd/engx/  && \
          go build -o ~/bin/engxa ./cmd/engxa/
test:     go test ./internal/... -count=1
db:       ~/.nexus/nexus.db
socket:   /tmp/engx.sock
http:     127.0.0.1:8080

### atlas
path:     ~/workspace/projects/apps/atlas
repo:     https://github.com/Harshmaury/Atlas
language: Go 1.25.0
type:     platform-service
domain:   Knowledge
purpose:  Workspace awareness — indexes files, docs, and architecture for AI context
status:   Phase 1 complete (v1.1.0) — workspace knowledge index running
          Phase 2 not started (graph, conflict detection)
run:      ~/bin/atlas &
build:    go build -o ~/bin/atlas ./cmd/atlas/
test:     go test ./internal/... -count=1
db:       ~/.nexus/atlas.db
http:     127.0.0.1:8081

### forge
path:     ~/workspace/projects/apps/forge
repo:     https://github.com/Harshmaury/Forge
language: Go 1.23.0
type:     platform-service
domain:   Execution
purpose:  Translates developer intent into coordinated platform actions
status:   Scaffold only — Phase 1 not started (requires Atlas Phase 1 ✅)
run:      ~/bin/forge &   (not yet built)
build:    go build -o ~/bin/forge ./cmd/forge/
test:     go test ./internal/... -count=1
http:     127.0.0.1:8082

### ums
path:     /mnt/c/Users/harsh/source/repos/AspireApp1
language: .NET 10, C#
type:     microservices (9 services)
purpose:  University Management System — separate concern from developer platform
infra:    Minikube, Kafka, PostgreSQL, YARP gateway
ports:    identity:5002 academic:5004 student:5003 attendance:5005
          examination:5006 fee:5007 faculty:5008 hostel:5009 notification:5010
obs:      Grafana:3000 Prometheus:9090 Jaeger:16686 Seq:8081
ci:       GitHub Actions → ~/actions-runner (self-hosted)

---

## PLATFORM STARTUP SEQUENCE

Start in this order. Atlas auto-reconnects to Nexus within 3 seconds:

```bash
engxd &
sleep 2
~/bin/atlas &
curl -s http://127.0.0.1:8080/health
curl -s http://127.0.0.1:8081/health
```

Atlas starts even if Nexus is down — it indexes from filesystem and retries
Nexus every 3 seconds. No restart needed once Nexus comes up.

---

## REGISTERING A PROJECT WITH NEXUS

```bash
# 1. Create .nexus.yaml in the project root
cat > /path/to/project/.nexus.yaml << 'EOF'
name: my-project
id: my-project
type: web-api
language: go
version: 1.0.0
EOF

# 2. Register (engxd must be running)
engx register /path/to/project

# 3. Verify with Nexus
engx project status my-project

# 4. Verify Atlas indexed it (within 3s)
curl -s http://127.0.0.1:8081/workspace/project/my-project
```

.nexus.yaml minimum required fields: name, type, language
Optional: id (derived from name if omitted), version, keywords (Drop Intelligence)

---

## GIT WORKFLOW

### Platform repos (nexus, atlas, forge)

  main              → always stable, always buildable
  phase<N>-<desc>   → feature branches (e.g. phase15-atlas-graph)
  commit format     → type: description
    types: feat | fix | refactor | test | docs | chore

  Apply command per project (substitute <project> and <branch>):
    cd ~/workspace/projects/apps/<project> && \
    go build ./... && \
    git add <files> WORKFLOW-SESSION.md && \
    git commit -m "<type>: <description>" && \
    git push origin <branch>

  go build ./... must pass before git add. Always.

### UMS repo

  master    → production
  CI/CD     → GitHub Actions, runner at ~/actions-runner

---

## CHANGELOG

2026-03-11  v1.0  Created (~/dev/nexus — old path)
2026-03-14  v2.0  Paths corrected to ~/workspace, split into 3 focused files
2026-03-15  v3.0  Added atlas + forge projects, all 4 drop folders, platform
                  startup sequence, correct Nexus phase status (14+ complete)
