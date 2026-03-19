# WORKFLOW-SESSION.md
# Session: NX-fix-bind-addr
# Date: 2026-03-19

## What changed

Fixed DefaultHTTPAddr from ":8080" (all interfaces) to "127.0.0.1:8080"
(loopback only). Closes audit issue #1 — security finding from full audit.

## Modified files
- internal/config/env.go    — DefaultHTTPAddr = "127.0.0.1:8080"

## Apply

cd ~/workspace/projects/apps/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-fix-bind-addr-20260319.zip -d . && \
go build ./...

## Verify

grep "DefaultHTTPAddr" internal/config/env.go
# Expected: const DefaultHTTPAddr = "127.0.0.1:8080"

## Commit

git add internal/config/env.go WORKFLOW-SESSION.md && \
git commit -m "fix: bind engxd to 127.0.0.1:8080 (audit #1)" && \
git push origin main
