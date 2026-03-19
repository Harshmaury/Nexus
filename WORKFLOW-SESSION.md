# WORKFLOW-SESSION.md
# Session: NX-goreleaser-pipeline
# Date: 2026-03-19

## What changed — goreleaser pipeline (ADR-030)

Adds the release pipeline that publishes signed tarballs to GitHub Releases
on every v* tag push. Produces engx-<version>-<os>-<arch>.tar.gz for
linux/{amd64,arm64}, darwin/{amd64,arm64}, and windows/amd64 (engx+engxa only).
Checksums manifest: engx-<version>-checksums.txt. Version strings injected via
ldflags so engx version and GET /health daemon_version reflect the release tag.

## New files

- `.goreleaser.yaml`                              — build + archive + checksum + release config
- `.github/workflows/release.yml`                 — Actions workflow: on push v* tags
- `architecture/decisions/ADR-030-goreleaser-pipeline.md` — pipeline contract

## Modified files

- `WORKFLOW-SESSION.md` — this file

## Apply

```bash
cd ~/workspace/projects/apps/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-goreleaser-20260319-HHMM.zip -d .
```

No `go build` required — these are CI/CD config files only.

## Verify

```bash
# Install goreleaser locally if not present
# curl -fsSL https://goreleaser.com/static/run | bash -s -- --version

# Dry-run locally (builds but does not publish)
goreleaser release --snapshot --clean

# Expected output:
#   • starting release
#   • loading config file   file=.goreleaser.yaml
#   • building binaries
#   • creating archives
#   • calculating checksums
#   • snapshoting
#   • storing release metadata
#   ✓ release succeeded after Xs

# Check produced artifacts
ls dist/
# engx-1.5.0-dev-linux-amd64.tar.gz
# engx-1.5.0-dev-linux-arm64.tar.gz
# engx-1.5.0-dev-checksums.txt
# ...

# Verify tarball layout matches install contract
tar -tzf dist/engx-*-linux-amd64.tar.gz
# bin/engxd
# bin/engx
# bin/engxa
# LICENSE
# README.md

# Trigger a real release
git tag v1.5.0-phase22   # (already tagged — use next version for real release)
git push origin --tags
# → Actions workflow fires, release appears at:
# https://github.com/Harshmaury/Nexus/releases
```

## Commit

```bash
git add \
  .goreleaser.yaml \
  .github/workflows/release.yml \
  architecture/decisions/ADR-030-goreleaser-pipeline.md \
  WORKFLOW-SESSION.md && \
git commit -m "chore(release): goreleaser pipeline + GitHub Actions workflow (ADR-030)" && \
git push origin main
```

Note: no version tag on this commit — the pipeline itself is not a versioned
feature of the platform runtime. The next feature tag (v1.6.0-phase23 or
similar) will be the first real goreleaser-published release.
