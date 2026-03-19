# ADR-029 — engx doctor Extended Filesystem Checks

**Status:** Accepted
**Date:** 2026-03-19
**Author:** Harsh Maury
**Scope:** nexus (engx CLI + engxd daemon)

---

## Context

`engx upgrade` (ADR-028) runs `engx doctor` as its mandatory preflight check
before swapping binaries. For upgrade to be a genuine safety gate, doctor must
verify more than API-level health — it must also catch local environment problems
that would cause a freshly swapped binary to misbehave or fail to start.

Five local checks are missing from the current doctor implementation:
- SQLite database may be corrupt (engxd refuses to start; upgrade would pass
  preflight only to have the daemon fail on next restart)
- Port conflicts from foreign processes block engxd binding
- CLI and daemon binary versions can diverge silently after a partial upgrade
- `~/.nexus/` world-writable permissions allow privilege escalation
- A stale service-tokens file (> 90 days old) poses a credential hygiene risk

---

## Decision

Add a new file `cmd/engx/cmd_doctor_fs.go` containing five local checks.
Extend `doctorReport` with a `fsChecks []doctorFSCheck` field. Wire the checks
into `collect()` and render them in `print()` between Forge and fetch errors.

### Five Checks

1. **db-integrity** — `sql.Open` the SQLite DB read-only, run
   `PRAGMA integrity_check`. Pass if result = `"ok"`. Skip if DB does not exist
   yet (first run). Fail-closed: any open/query error is a failure.

2. **port-conflicts** — TCP probe ports 8080–8087. If bound, identify the holder
   via `/proc/<pid>/cmdline` (Linux) or skip identification (non-Linux). Flag any
   port held by a non-engx process. Uses only stdlib — no `lsof` dependency.

3. **binary-versions** — `GET /health` now returns `daemon_version`. Compare
   against `cliVersion` constant in `cmd/engx/main.go`. Mismatch is a warning
   with `engx upgrade` as the suggested fix. Daemon unreachable = skip, not fail.

4. **nexus-perms** — `os.Stat("~/.nexus/")`. Flag if `mode & 0o022 != 0`
   (group-write or world-write set). Suggest `chmod go-w ~/.nexus/`.

5. **token-age** — `os.Stat("~/.nexus/service-tokens")`. Warn if `ModTime`
   is more than 90 days ago. Not a hard failure — operator may choose to keep
   long-lived tokens.

### Daemon Change

`ServerConfig.DaemonVersion string` added to `internal/api/ServerConfig`.
`handleHealth` replaced by `makeHealthHandler(daemonVersion string)` factory.
Response gains `"daemon_version"` field. `/health` remains exempt from auth
(ADR-008 unchanged). Old clients that ignore unknown JSON fields are unaffected.

### Rules

1. All five checks are local — no new upstream HTTP calls beyond the existing
   `/health` call already made by doctor.
2. Each check is a `doctorFSCheck{name, ok, message}` — uniform, renderable.
3. A check that cannot run (home dir unresolvable, tool absent) reports ok=true
   with a `"skipped"` message — never blocks the preflight exit code.
4. `cmd_doctor_fs.go` is a separate file; `main.go` is not re-opened for logic.
5. `collectFS` total line count ≤ 40. Each helper ≤ 40 lines.

---

## Compliance

| ADR     | Status |
|---------|--------|
| ADR-003 | ✅ No new inter-service calls |
| ADR-008 | ✅ `/health` remains auth-exempt |
| ADR-028 | ✅ Doctor is now a genuine preflight safety gate |

---

## Next ADR

ADR-030 — goreleaser pipeline and GitHub Actions release workflow
