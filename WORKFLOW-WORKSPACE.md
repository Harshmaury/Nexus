# WORKFLOW-WORKSPACE.md
# Workspace layout, projects, integration guide, git workflow
# @version: 2.0.0
# @updated: 2026-03-14

---

## WORKSPACE

```
~/
├── bin/                          engx, engxd, kubectl, minikube (globally available)
├── actions-runner/               Self-hosted GitHub Actions runner (UMS CI/CD)
├── go/                           Go toolchain (auto-managed)
└── workspace/
    ├── AI_CONTEXT.md/            Architecture constraints + AI rules (5 sections)
    ├── projects/
    │   ├── apps/
    │   │   └── nexus/            ← THIS REPO
    │   ├── Test/
    │   │   └── nexus-prototype/  Prototype reference — do not modify
    │   ├── ai/                   AI agent platform (future)
    │   ├── api/                  API services (future)
    │   ├── automation/           Automation scripts (future)
    │   ├── cli/                  CLI tools (future)
    │   └── archive/              Inactive projects
    ├── experiments/
    ├── datasets/
    ├── models/
    ├── shared/
    └── templates/
```

Drop folder:
  Windows:  C:\Users\harsh\Downloads\nexus-drop\
  WSL2:     /mnt/c/Users/harsh/Downloads/nexus-drop/

---

## PROJECTS

### nexus
path:     ~/workspace/projects/apps/nexus
language: Go 1.24.1
type:     platform-daemon
purpose:  Local developer control plane — orchestrates all other projects
status:   Phase 8 complete. Phase 9 (process/k8s providers) is next.
run:      go run ./cmd/engxd/
build:    go build -o ~/bin/engxd ./cmd/engxd/ && go build -o ~/bin/engx ./cmd/engx/
test:     go test ./internal/... -count=1
db:       ~/.nexus/nexus.db
socket:   /tmp/engx.sock
http:     127.0.0.1:8080

### ums
path:     /mnt/c/Users/harsh/source/repos/AspireApp1
language: .NET 10, C#
type:     microservices (9 services)
purpose:  University Management System
infra:    Minikube, Kafka, PostgreSQL, YARP gateway
ports:    identity:5002 academic:5004 student:5003 attendance:5005
          examination:5006 fee:5007 faculty:5008 hostel:5009 notification:5010
obs:      Grafana:3000 Prometheus:9090 Jaeger:16686 Seq:8081
ci:       GitHub Actions → ~/actions-runner (self-hosted)

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

# 2. Register (daemon must be running)
engx register /path/to/project

# 3. Verify
engx project status my-project
```

.nexus.yaml minimum required fields: name, type, language
Optional: id (derived from name if omitted), version, keywords (used by Drop detector)

---

## GIT WORKFLOW

Nexus repo:
  main            → always stable, always buildable
  phase<N>-<name> → feature branches (e.g. phase9-providers)
  commit format   → type: description
    types: feat | fix | refactor | test | docs | chore

One-liner after each component:
  git add . && git commit -m "feat: [component]" && git push origin HEAD

UMS repo:
  master          → production
  CI/CD           → GitHub Actions, runner at ~/actions-runner

---

## CHANGELOG

2026-03-11  v1.0  Created (~/dev/nexus — old path)
2026-03-14  v2.0  Paths corrected to ~/workspace, split into 3 focused files
