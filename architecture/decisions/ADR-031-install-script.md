# ADR-031 — scripts/install.sh Zero-to-Running Installer

**Status:** Accepted
**Date:** 2026-03-19
**Author:** Harsh Maury
**Scope:** nexus (distribution — not a runtime service)

---

## Context

The goreleaser pipeline (ADR-030) publishes tarballs to GitHub Releases.
Without an install script, new users must manually find the right tarball,
verify the checksum, extract it, add ~/bin/ to PATH, and register engxd as
a system service. The Wave 4 goal is zero-to-running in under 2 minutes.

---

## Decision

Add `scripts/install.sh` — a portable bash script hosted at `get.engx.dev`.
Invoked via: `curl -fsSL https://get.engx.dev/install.sh | bash`

### Install sequence (8 steps)

1. Detect OS and architecture (linux/darwin × amd64/arm64)
2. Resolve latest release tag from GitHub Releases API
3. Download platform tarball to a temp directory
4. Verify SHA256 against the checksums manifest (sha256sum or shasum)
5. Extract `bin/engxd`, `bin/engx`, `bin/engxa` to `~/bin/`
6. Add `~/bin/` to PATH in `~/.bashrc` or `~/.zshrc` if not already present
7. Run `engx platform install` to register engxd as launchd/systemd service
8. Print summary with next steps

### Rules

1. Temp directory is always cleaned up via `trap ... EXIT` — no orphaned files.
2. Checksum verification is mandatory — script exits on mismatch.
3. Step 5 uses `install -m 755` — binaries are always executable.
4. Step 7 is best-effort — failure is a warning, not a fatal error.
5. `ENGX_CHANNEL=beta` env var or `--channel beta` flag targets pre-releases.
6. Windows is not supported — script exits with a clear message.
7. The script is shellcheck-clean (SC2015, SC2016, SC2059 all resolved).

### Naming contract dependency (ADR-028 / ADR-030)

The script constructs tarball URLs as:
`https://github.com/Harshmaury/Nexus/releases/download/<tag>/engx-<version>-<os>-<arch>.tar.gz`

This matches `internal/upgrade/release.go` exactly. Any change to the goreleaser
naming template requires a coordinated update here.

---

## Compliance

| ADR     | Status |
|---------|--------|
| ADR-028 | ✅ Same tarball URL and naming convention |
| ADR-030 | ✅ Consumes goreleaser output directly |

---

## Next ADR

ADR-032 — Homebrew tap formula (`github.com/Harshmaury/homebrew-engx`)
