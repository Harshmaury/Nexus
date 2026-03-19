# WORKFLOW-SESSION.md
# Session: NX-phase22-doctor-extended
# Date: 2026-03-19

## What changed — Nexus Phase 21+22: engx upgrade (ADR-028) + doctor extended (ADR-029)

Phase 21: adds `engx upgrade` — self-update command that downloads the latest
GitHub release, verifies SHA256, runs doctor preflight, then atomically swaps
engxd/engx/engxa in ~/bin/. No new dependencies (stdlib only).

Phase 22: extends `engx doctor` with five local filesystem checks: SQLite
integrity, port conflicts, binary version cross-check (CLI vs daemon), ~/.nexus/
permissions, and service-tokens age warning. GET /health now returns
daemon_version for the version cross-check. Doctor is now a genuine preflight
safety gate for engx upgrade.

## New files

- `architecture/decisions/ADR-029-doctor-extended-checks.md` — extended doctor rules
- `cmd/engx/cmd_doctor_fs.go`    — five local FS checks (collectFS, printFS)
- `cmd/engx/cmd_upgrade.go`      — engx upgrade command (ADR-028)
- `internal/upgrade/release.go`  — GitHub Releases API resolution
- `internal/upgrade/verifier.go` — SHA256 checksum verification
- `internal/upgrade/installer.go`— download, extract, preflight, atomic swap
- `internal/upgrade/platform.go` — OS/arch detection

## Modified files

- `cmd/engx/main.go`        — upgradeCmd wired; doctorReport.fsChecks added;
                              collectFS + printFS wired; cliVersion → 1.5.0
- `cmd/engxd/main.go`       — DaemonVersion passed into api.ServerConfig
- `internal/api/server.go`  — ServerConfig.DaemonVersion; makeHealthHandler
                              replaces handleHealth; daemon_version in /health
- `nexus.yaml`              — version: 1.5.0-phase22

## Apply

```bash
cd ~/workspace/projects/apps/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-phase22-doctor-extended-20260319-HHMM.zip -d . && \
go build ./...
```

## Verify

```bash
# Build check
go build ./...

# Doctor shows new FS checks section
go run ./cmd/engx doctor

# Expected new lines in output:
#   ✓ db-integrity
#   ✓ port-conflicts
#   ✓ binary-versions      engx=1.5.0 engxd=1.5.0   (or "daemon unreachable — skipped")
#   ✓ nexus-perms
#   ✓ token-age            N days old

# Upgrade dry-run (requires live internet to GitHub)
go run ./cmd/engx upgrade --dry-run

# Version output
go run ./cmd/engx version
# engx version 1.5.0

# Health endpoint now includes daemon_version
curl -s http://127.0.0.1:8080/health | jq
# { "ok": true, "status": "healthy", "daemon_version": "0.1.0" }
```

## Commit

```bash
git add \
  architecture/decisions/ADR-029-doctor-extended-checks.md \
  cmd/engx/cmd_doctor_fs.go \
  cmd/engx/cmd_upgrade.go \
  cmd/engx/main.go \
  cmd/engxd/main.go \
  internal/api/server.go \
  internal/upgrade/release.go \
  internal/upgrade/verifier.go \
  internal/upgrade/installer.go \
  internal/upgrade/platform.go \
  nexus.yaml \
  WORKFLOW-SESSION.md && \
git commit -m "feat(phase21+22): engx upgrade (ADR-028) + doctor extended checks (ADR-029)" && \
git tag v1.5.0-phase22 && \
git push origin main --tags
```
