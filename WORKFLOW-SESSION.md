# WORKFLOW-SESSION.md
# @version: 2.5.0
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

✅ Phase 1   Daemon core
✅ Phase 2   Drop intelligence (4-layer detector)
✅ Phase 3   Bug fixes
✅ Phase 7   Internal hardening
✅ Phase 8   REST API (127.0.0.1:8080)
✅ Phase 9   Runtime providers (Process, K8s)
✅ Phase 10  Drop approve/reject
✅ Phase 11  Dependency graph (Kahn topo sort)
✅ Phase 12  Observability (metrics, engx watch)

✅ Phase 13  Drop Intelligence v2 (2026-03-15)
  internal/intelligence/classifier.go   Naive Bayes, tokenise, sigmoid confidence
                                         Train() from download_log, JSON persistence
                                         ~/.nexus/classifier.json
  internal/intelligence/detector.go     Layer 5 (ML) added, weight=0.30
                                         NewDetector now accepts *Classifier
  internal/state/storer.go              GetRecentDownloads added to interface
  internal/daemon/server.go             CmdDropTrain handler
                                         Classifier injected via ServerConfig

---

## DROP TRAIN WORKFLOW

  engx drop train

  Daemon reads download_log (action=moved|approved, up to 2000 rows)
  → Multinomial Naive Bayes trained on filename tokens per project
  → Model saved to ~/.nexus/classifier.json
  → Returns: examples_used, vocab_size, project_docs, trained_at

  Layer 5 activates automatically on next file drop.
  Re-train any time more files have been routed.

## MODEL LOCATION

  ~/.nexus/classifier.json   (created by engx drop train)
  ~/.nexus/pids/             (process provider PID files)
  ~/.nexus/logs/             (process provider stdout/stderr)
  ~/.nexus/nexus.db          (SQLite state store)

---

## ROADMAP

Phase 14 — Multi-machine agent mode (gRPC, remote state sync)

---

## CHANGELOG

2026-03-11  v1.0  Created
2026-03-14  v2.0–2.2  Phases 9–10
2026-03-15  v2.3  Phase 11 — dependency graph
2026-03-15  v2.4  Phase 12 — metrics + engx watch
2026-03-15  v2.5  Phase 13 — Naive Bayes classifier, engx drop train
