# WORKFLOW-SESSION.md
# @version: 3.1.0
# @updated: 2026-03-16
# @repo: https://github.com/Harshmaury/Nexus

---

## Start a Session

```bash
cd ~/workspace/projects/apps/nexus && ./scripts/verify.sh
```

Paste output into Claude. Session key format: `NX-<hash>-<YYYYMMDD>`

---

## Identity

Developer: Harsh Maury | OS: Ubuntu 24.04 (WSL2) + Windows 11
Go: 1.24.1 | Drop folder: /mnt/c/Users/harsh/Downloads/engx-drop/

---

## Platform

```
Control    Nexus  :8080  ← this
Knowledge  Atlas  :8081
Execution  Forge  :8082
```

---

## Build Status
# Last verified: 2026-03-16

✅ Phases 1–14            Complete — full control plane
✅ ADR-002                Workspace observation (5 topics)
✅ ADR-008                Inter-service auth (ServiceAuth middleware)
✅ v1.0.0-fixes-complete  All criticals + highs resolved

---

## Environment Variables

```
NEXUS_DB_PATH             ~/.nexus/nexus.db
NEXUS_SOCKET              /tmp/engx.sock
NEXUS_HTTP_ADDR           :8080
NEXUS_DROP_DIR            ~/nexus-drop
NEXUS_WORKSPACE           ~/workspace
NEXUS_RECONCILE_INTERVAL  5s
NEXUS_HEALTH_INTERVAL     10s
NEXUS_HEALTH_TIMEOUT      5s
```

---

## Key Files

```
internal/config/service_tokens.go      LoadServiceTokens — reads ~/.nexus/service-tokens
internal/api/middleware/service_auth.go ServiceAuth middleware (ADR-008)
internal/state/db.go                   allMigrations — single source (v1–v4)
internal/daemon/engine.go              publishResult (context-aware), topoSort (O(n+e))
internal/intelligence/classifier.go    Classifier (RWMutex, modelDir-based paths)
internal/agent/client.go               re-registers on heartbeat failure only
pkg/events/topics.go                   public topic constants for Atlas + Forge
```

---

## Roadmap

Feature-complete. Future work is ADR-driven.
Next open item: Nexus `GET /events` `?since=` parameter (X1 gap).

---

## Commands

All commands in `~/workspace/developer-platform/RUNBOOK.md`.

---

## Changelog

2026-03-16  v3.1.0  session doc simplified — commands moved to RUNBOOK.md
2026-03-16  v3.0.0  All criticals + highs fixed, ADR-008 implemented
2026-03-15  v2.8.0  ADR-002 workspace observation
