# WORKFLOW-SESSION.md
# @version: 2.6.0
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

Format: NX-<git-short-hash>-<YYYYMMDD>
Claude: fetch this file → match hash → confirm → ask for task.

---

## IDENTITY

Developer: Harsh Maury  |  GitHub: https://github.com/Harshmaury
Nexus: https://github.com/Harshmaury/Nexus
OS: Ubuntu 24.04 (WSL2) + Windows 11

---

## MACHINE
# Update only when a tool is installed or upgraded

Go:1.24.1  Python:3.12.3  Node:22.22.0  .NET:10.0.103
Docker:28.2.2  kubectl:v1.35.1  Minikube:v1.38.1  Git:2.43.0

---

## BUILD STATUS
# Last verified: 2026-03-15

✅ Phase 1   Daemon core (store, bus, engine, controllers, socket, CLI)
✅ Phase 2   Drop intelligence (4-layer detector)
✅ Phase 3   Bug fixes (go.mod, Storer interface, notifier)
✅ Phase 7   Internal hardening
✅ Phase 8   REST API (127.0.0.1:8080)
✅ Phase 9   Runtime providers (Process, K8s)
✅ Phase 10  Drop approve/reject
✅ Phase 11  Dependency graph (Kahn topo sort)
✅ Phase 12  Observability (metrics + engx watch)
✅ Phase 13  Drop Intelligence v2 (Naive Bayes, engx drop train)

✅ Phase 14  Multi-machine agent mode (2026-03-15)
  internal/state/db_agents.go        migration v4: agents table
  internal/state/storer.go           RegisterAgent, HeartbeatAgent,
                                     GetAgent, GetAllAgents in interface
  internal/agent/client.go           engxa agent loop: register, sync,
                                     heartbeat, local reconcile
  internal/api/handler/agents.go     /agents routes: register, heartbeat,
                                     desired, actual, list
  internal/api/server.go             agent routes wired into HTTP server
  cmd/engxa/main.go                  standalone agent binary
  cmd/engx/main.go                   engx agents command

---

## AGENT USAGE

On remote machine:
  go build -o ~/bin/engxa ./cmd/engxa/
  engxa --server http://192.168.1.10:8080 --token <secret>

Central machine:
  engx agents                        list registered agents + online status

Assign a service to a remote agent (in svc.Config JSON):
  {"image":"postgres:16", "agent_id":"my-server-1"}

The service will be routed to agent "my-server-1" and reconciled there.
The central store remains the source of truth for desired state.

---

## ALL BINARIES

  ~/bin/engxd   central daemon
  ~/bin/engx    CLI client
  ~/bin/engxa   remote agent
  ~/bin/kubectl
  ~/bin/minikube

---

## ROADMAP

All planned phases complete. Future work as needed:
  - API key auth (Phase 8 remainder — ~/.nexus/api_key header validation)
  - engx drop train watch mode (auto-retrain on N new log entries)
  - TLS for agent↔server communication
  - Prometheus scrape target (swap JSON metrics for client_golang)

---

## CHANGELOG

2026-03-11  v1.0  Created
2026-03-14  v2.0–v2.2  Phases 9–10
2026-03-15  v2.3  Phase 11 — dependency graph
2026-03-15  v2.4  Phase 12 — metrics + watch
2026-03-15  v2.5  Phase 13 — Naive Bayes classifier
2026-03-15  v2.6  Phase 14 — multi-machine agent mode (all phases complete)
