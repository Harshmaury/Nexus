# ADR-030 — goreleaser Release Pipeline

**Status:** Accepted
**Date:** 2026-03-19
**Author:** Harsh Maury
**Scope:** nexus (CI/CD — not a runtime service)

---

## Context

`engx upgrade` (ADR-028) resolves release metadata from the GitHub Releases API
and downloads tarballs by a naming convention agreed at design time. Without a
real release pipeline producing tarballs in that exact format, upgrade has
nothing to fetch. Wave 4 requires a published binary before the install script
and Homebrew tap can be written.

---

## Decision

Add `.goreleaser.yaml` in the nexus repo root and a GitHub Actions workflow at
`.github/workflows/release.yml`. Together they produce a GitHub Release on every
`v*` tag push.

### Naming contract (must not change without updating ADR-028)

```
Tarball:   engx-<version>-<os>-<arch>.tar.gz
Checksums: engx-<version>-checksums.txt
```

`<os>`   = `linux` | `darwin` | `windows`
`<arch>` = `amd64` | `arm64`

These values are `runtime.GOOS` and `runtime.GOARCH` as produced by goreleaser
`{{ .Os }}` / `{{ .Arch }}` template variables. The upgrade client in
`internal/upgrade/release.go` constructs URLs using these same values directly.

### Tarball layout (must not change without updating scripts/install.sh)

```
bin/engxd
bin/engx
bin/engxa
LICENSE
README.md
```

`wrap_in_directory: false` — files land at the root of the archive. The upgrade
installer extracts by base name match; install.sh extracts flat into a temp dir.

### Build matrix

| Binary | CGO | Platforms |
|--------|-----|-----------|
| engxd  | on  | linux/{amd64,arm64}, darwin/{amd64,arm64} |
| engx   | off | linux/{amd64,arm64}, darwin/{amd64,arm64}, windows/amd64 |
| engxa  | off | linux/{amd64,arm64}, darwin/{amd64,arm64}, windows/amd64 |

Darwin builds run on `macos-latest` (native runner) in a parallel job because
CGO cross-compilation for darwin from Linux requires osxcross toolchain setup.
Linux builds run in goreleaser on `ubuntu-latest`.

### Version injection

goreleaser injects version via `-ldflags`:
- engxd: `-X main.daemonVersion={{.Version}}`
- engx:  `-X main.cliVersion={{.Version}}`

These match the constant names in `cmd/engxd/main.go` and `cmd/engx/main.go`.
After injection, `engx version` and `GET /health daemon_version` will both
reflect the release tag version, making the binary version cross-check
(ADR-029) accurate for distributed installs.

### Pre-release detection

goreleaser `prerelease: auto` — tags containing `-rc`, `-beta`, or `-alpha`
are automatically marked as pre-release on GitHub. `engx upgrade --channel beta`
targets the latest pre-release; `--channel stable` (default) skips them.

### Checksums merge

goreleaser produces checksums for Linux/Windows builds. The `build-darwin` job
produces separate darwin checksums, then uploads a merged file under the same
`engx-<version>-checksums.txt` name. The upgrade verifier fetches this single
file regardless of client platform.

### Rules

1. The tarball naming template `engx-{{ .Version }}-{{ .Os }}-{{ .Arch }}`
   is a contract. Any change requires a coordinated update to ADR-028 and
   `internal/upgrade/release.go`.
2. `wrap_in_directory: false` is permanent — the upgrade installer and
   install.sh both extract by base name.
3. The `GITHUB_TOKEN` secret is the only required secret. No external signing
   keys until cosign is added (future ADR).
4. Snapshot builds (`goreleaser release --snapshot`) produce dev-tagged
   artifacts locally but are never published.
5. Windows receives `engx` and `engxa` only — `engxd` is not supported on
   Windows (ADR-028 Rule 8 by extension).

---

## Compliance

| ADR     | Status |
|---------|--------|
| ADR-028 | ✅ Naming convention implemented exactly |
| ADR-029 | ✅ Version injection enables binary version cross-check |

---

## Next ADR

ADR-031 — `scripts/install.sh` zero-to-running installer
