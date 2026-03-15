# WORKFLOW-SESSION.md
# @version: 3.0.0
# @updated: 2026-03-16
# @repo: https://github.com/Harshmaury/Nexus

---

## START A SESSION

```bash
cd ~/workspace/projects/apps/nexus && ./scripts/verify.sh
```

Paste the output into Claude. Claude reads hash, confirms, asks for task.

---

## SESSION KEY

Format: `NX-<git-short-hash>-<YYYYMMDD>`

---

## IDENTITY

Developer: Harsh Maury  |  GitHub: https://github.com/Harshmaury
OS: Ubuntu 24.04 (WSL2) + Windows 11
Go: 1.24.1  Docker: 28.2.2  kubectl: v1.35.1  Minikube: v1.38.1

---

## PLATFORM

```
Control    Nexus   ~/workspace/projects/apps/nexus   :8080  ← this
Knowledge  Atlas   ~/workspace/projects/apps/atlas   :8081
Execution  Forge   ~/workspace/projects/apps/forge   :8082
```

---

## BUILD STATUS
# Last verified: 2026-03-16

✅ Phases 1–14            Complete — full control plane
✅ ADR-002                Workspace observation (watcher + 5 topics)
✅ v1.0.0-fixes-complete  All criticals + highs resolved

Tag: v1.0.0-fixes-complete → commit 5d36a8c

---

## ENVIRONMENT VARIABLES

  NEXUS_DB_PATH             ~/.nexus/nexus.db
  NEXUS_SOCKET              /tmp/engx.sock
  NEXUS_HTTP_ADDR           :8080
  NEXUS_DROP_DIR            ~/nexus-drop
  NEXUS_WORKSPACE           ~/workspace
  NEXUS_RECONCILE_INTERVAL  5s
  NEXUS_HEALTH_INTERVAL     10s
  NEXUS_HEALTH_TIMEOUT      5s

---

## BUILD + RUN

```bash
go build -o ~/bin/engxd ./cmd/engxd/
go build -o ~/bin/engx  ./cmd/engx/
go build -o ~/bin/engxa ./cmd/engxa/
engxd &
```

---

## WORKSPACE EVENT TOPICS

Import: github.com/Harshmaury/Nexus/internal/eventbus
Never redefine topic strings locally.

  TopicWorkspaceFileCreated      workspace.file.created
  TopicWorkspaceFileModified     workspace.file.modified
  TopicWorkspaceFileDeleted      workspace.file.deleted
  TopicWorkspaceUpdated          workspace.updated
  TopicWorkspaceProjectDetected  workspace.project.detected

---

## ROADMAP

Feature-complete. Future work is ADR-driven only.

---

## CHANGELOG

2026-03-16  v3.0.0  All criticals + highs fixed, tagged v1.0.0-fixes-complete
2026-03-15  v2.8.0  ADR-002 workspace observation
2026-03-14  v2.0.0  Phases 9–14 complete
