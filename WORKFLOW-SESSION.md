# WORKFLOW-SESSION.md
# Session: NX-install-script
# Date: 2026-03-19

## What changed — install script (ADR-031)

Adds scripts/install.sh — zero-to-running installer for the engx platform.
Detects OS/arch, resolves latest release from GitHub, downloads and verifies
the SHA256 checksum, installs binaries to ~/bin/, configures PATH, and
registers engxd as a launchd/systemd service via engx platform install.
Shellcheck-clean. Supports --channel beta flag and ENGX_CHANNEL env var.

## New files

- `scripts/install.sh`                               — installer script
- `architecture/decisions/ADR-031-install-script.md` — install contract

## Modified files

- `WORKFLOW-SESSION.md` — this file

## Apply

```bash
cd ~/workspace/projects/apps/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-install-script-20260319-HHMM.zip -d .
chmod +x scripts/install.sh
```

## Verify

```bash
# Dry-run: test platform detection and release resolution only
# (set ENGX_CHANNEL to avoid real download if desired)
bash -x scripts/install.sh --channel stable 2>&1 | head -40

# Full local test (requires a published GitHub release):
bash scripts/install.sh

# Expected output:
#   engx installer  channel=stable
#   → resolving latest stable release...
#   ✓ found v1.5.0-phase22
#   → downloading engx-1.5.0-phase22-linux-amd64.tar.gz...
#   ✓ downloaded
#   → verifying SHA256 checksum...
#   ✓ checksum verified
#   → extracting to ~/bin/...
#   ✓ installed engxd
#   ✓ installed engx
#   ✓ installed engxa
#   → adding ~/bin to PATH in ~/.bashrc...
#   ✓ PATH updated
#   → registering engxd as a system service...
#   ✓ engxd registered — will auto-start on login
#
#   engx 1.5.0-phase22 installed successfully.
```

## Commit

```bash
git add \
  scripts/install.sh \
  architecture/decisions/ADR-031-install-script.md \
  WORKFLOW-SESSION.md && \
git commit -m "chore(release): zero-to-running install script (ADR-031)" && \
git push origin main
```
