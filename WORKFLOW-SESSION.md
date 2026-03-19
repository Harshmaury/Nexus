# WORKFLOW-SESSION.md
# Session: NX-phase20-reliability
# Date: 2026-03-19

## What changed — Nexus Phase 20 (fail-safe system — items 1-3)

Three reliability improvements aligned with the fail-safe design:

1. SQLite integrity check at startup (F-6: store corruption).
   New() runs PRAGMA integrity_check before migrations. On failure:
   backs up the corrupt file as nexus.db.corrupt-<timestamp>, returns
   an error, and engxd refuses to start with a clear message.

2. Rolling daily backup of the clean database.
   On every clean startup, copies nexus.db to
   ~/.nexus/backups/nexus-YYYY-MM-DD.db. Keeps last 3 daily backups.
   Best-effort — never blocks startup.

3. safeRun() goroutine wrapper in engxd.
   Replaces the 7 raw goroutine launch blocks with safeRun().
   Catches panics in any component and converts them to clean shutdown
   via errCh, rather than silently killing the process.

4. bootstrapNexusHome fail-closed (change 2 in patch).
   A broken ~/.nexus/ directory now causes engxd to refuse startup
   with a clear error, rather than logging a warning and continuing
   with a broken log directory.

## New files
- (none)

## Modified files
- internal/state/db.go   — checkIntegrity(), backupDB(), rollingBackup(),
                           pruneBackups() added; New() wired with integrity
                           check + rolling backup before migrate()
- cmd/engxd/main.go      — safeRun() added; 7 goroutine blocks replaced;
                           bootstrapNexusHome fail-closed (see MAIN_GO_PATCH.md)

## Apply

cd ~/workspace/projects/apps/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-phase20-reliability-20260319.zip -d .

# Apply MAIN_GO_PATCH.md to cmd/engxd/main.go using the script in the patch file.

go build ./... && echo "build ok"

## Verify

# Integrity check works on clean DB:
go build ./cmd/engxd/ && engxd &
# Should start normally, backup created:
ls ~/.nexus/backups/

# safeRun wired:
grep "safeRun" cmd/engxd/main.go | wc -l
# Expected: 7

# bootstrapNexusHome fail-closed:
grep "fix directory permissions" cmd/engxd/main.go
# Expected: 1 line

## Commit

git add \
  internal/state/db.go \
  cmd/engxd/main.go \
  WORKFLOW-SESSION.md && \
git commit -m "feat(phase20): SQLite integrity check + rolling backup + safeRun panic recovery" && \
git tag v1.4.0-phase20 && \
git push origin main --tags
