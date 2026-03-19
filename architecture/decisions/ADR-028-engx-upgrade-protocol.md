# ADR-028 — engx Self-Upgrade Protocol

**Status:** Accepted
**Date:** 2026-03-19
**Author:** Harsh Maury
**Scope:** nexus (engx CLI only)

---

## Context

Wave 4 adds a `goreleaser` pipeline that publishes signed tarballs to GitHub
Releases on every `v*` tag. Without a self-upgrade command, users must
manually download, verify, and swap binaries. This creates friction and
means production installs drift behind head.

The upgrade command must be safe enough to run on a live platform — it cannot
take the running engxd offline, and it must not leave the system in a partial
state if any step fails.

---

## Decision

Add `engx upgrade` as a CLI-only command. It never calls engxd and never
modifies running daemon state.

### Upgrade Protocol

```
1. Resolve latest release version from GitHub Releases API
2. Build asset URLs from the goreleaser naming convention
3. Download tarball to a temp file (streamed, not buffered in memory)
4. Download checksums manifest and verify SHA256 of the tarball
5. Extract the three binaries (engxd, engx, engxa) to a temp directory
6. Preflight: run <tmpdir>/engx doctor — must exit 0 against the live engxd
7. Atomic swap: os.Rename each binary from temp dir to ~/bin/
8. Print new version and next steps
```

Steps 1–6 are fully reversible — temp files are cleaned up on any failure.
Step 7 is the only irreversible step, and it only runs after all checks pass.

### Rules

1. **Never call engxd during upgrade.** engx upgrade communicates only with
   `api.github.com` and the local filesystem.

2. **Checksum verification is mandatory.** If the checksums file cannot be
   fetched or the SHA256 does not match, the upgrade aborts before extraction.

3. **Preflight is mandatory.** The new `engx doctor` must exit 0 before any
   binary is swapped. A non-zero exit aborts the upgrade with a clear message.

4. **Atomic swap via `os.Rename`.** Source and destination are always on the
   same filesystem (`~/bin/`). Rename is atomic on all supported platforms.

5. **`--dry-run` shows every step without writing anything.** Output is
   identical to a real run up to and including the preflight check result.

6. **`--channel beta` targets the latest pre-release tag.** Default channel is
   `stable` (latest non-pre-release). No other channels are defined.

7. **Binary locations follow the existing install convention.** Binaries are
   installed to `~/bin/` — the same directory `engx platform install` uses.
   No sudo. No system directories.

8. **Windows is not supported by the upgrade command.** Windows users should
   use the `.zip` from GitHub Releases directly.

### Asset URL Convention (goreleaser output)

```
Tarball:   https://github.com/Harshmaury/Nexus/releases/download/<tag>/engx-<ver>-<os>-<arch>.tar.gz
Checksums: https://github.com/Harshmaury/Nexus/releases/download/<tag>/engx-<ver>-checksums.txt
```

`<os>` = `linux` | `darwin`
`<arch>` = `amd64` | `arm64`

---

## Compliance

| ADR     | Status |
|---------|--------|
| ADR-003 | ✅ Compatible — upgrade command makes no inter-service calls |
| ADR-019 | ✅ Compatible — zp is unaffected |
| ADR-026 | ✅ Compatible — after swap, `engx platform install` is unchanged |

---

## Next ADR

ADR-029 — install script (`scripts/install.sh`) and Homebrew tap protocol
