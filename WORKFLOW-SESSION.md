# WORKFLOW-SESSION.md
# Session: nexus-fix-canon-events-enforce-20260325
# Date: 2026-03-25

## What changed — Nexus fix: GAP-1 + GAP-2 + GAP-3

Three architectural gaps closed:
GAP-1: Canon bumped from v0.4.1 → v1.0.0 (required by ADR-045; Relay constants
and workspace payload types were missing from v0.4.1).
GAP-2: EventWriter.write() now propagates level and parentSpanID instead of
hardcoding "info" and "" — ADR-037 causality tree is now functional.
GAP-3: resolveProjectDir() reads ~/.nexus/platform-paths.json first (ADR-032 §4)
before falling back to the conventional path, preventing silent Arbiter bypass
on non-standard installs.

## New files
- (none)

## Modified files
- `go.mod`                          — Canon v0.4.1 → v1.0.0
- `internal/state/events.go`        — write() signature adds parentSpanID + level;
                                      ServiceCrashed now emits level="error";
                                      SystemAlert passes severity as level;
                                      all other typed methods pass "" + "info"
- `cmd/engx/cmd_enforce.go`         — resolveProjectDir reads platform-paths.json
                                      first; loadPlatformPath() helper added;
                                      encoding/json import added

## Apply

cd ~/workspace/projects/engx/services/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-fix-canon-events-enforce-20260325.zip -d . && \
go mod tidy && \
go build ./...

## Verify

go test ./internal/state/...
go test ./internal/plan/...
go build ./cmd/engx/
go build ./cmd/engxd/

## Commit

git add go.mod go.sum \
  internal/state/events.go \
  cmd/engx/cmd_enforce.go \
  WORKFLOW-SESSION.md && \
git commit -m "fix: Canon v1.0.0 (ADR-045), event level+parentSpanID propagation (ADR-037), resolveProjectDir reads platform-paths (ADR-032)" && \
git tag v1.5.1-fixes && \
git push origin main --tags
