# WORKFLOW-SESSION.md
# Session: NX-phase18-19-install-perms
# Date: 2026-03-19

## What changed — Nexus Phase 18 + 19

Phase 18 (ADR-026): engx platform install/uninstall/log commands.
  After install, engxd starts automatically at login — no more "engxd &".
  macOS: launchd LaunchAgent in ~/Library/LaunchAgents/
  Linux: systemd user service in ~/.config/systemd/user/

Phase 19: Token file permission enforcement + ~/.nexus/ bootstrap.
  engxd now creates ~/.nexus/logs/ and ~/.nexus/backups/ at startup.
  Warns at startup if service-tokens file is world-readable (not 0600).

## New files
- cmd/engx/cmd_install.go     — platformInstallCmd, platformUninstallCmd,
                                 platformServiceLogsCmd, launchd/systemd logic

## Modified files
- cmd/engx/main.go             — see MAIN_GO_PATCH.md Part A (1 change)
- cmd/engxd/main.go            — see MAIN_GO_PATCH.md Part B (3 changes)

## Apply

cd ~/workspace/projects/apps/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-phase18-19-install-perms-20260319.zip -d .

Then apply MAIN_GO_PATCH.md (Part A to cmd/engx/main.go, Part B to cmd/engxd/main.go).

go build ./cmd/engx/ ./cmd/engxd/ && echo "build ok"

## Verify Phase 18

./engx platform --help
# Should show: install, uninstall, log

./engx platform install
# Should print:
#   ✓ systemd unit written: ~/.config/systemd/user/engxd.service
#   ✓ engxd enabled — will start automatically at login

# Confirm service is running:
systemctl --user status engxd

./engx platform log
# Should show recent engxd output

## Verify Phase 19

pkill engxd
go install ./cmd/engxd/ && cp ~/go/bin/engxd ~/bin/engxd && engxd &
# Should see in log:
#   opening state store: ~/.nexus/nexus.db
# If service-tokens is 0644, should also see:
#   WARNING: token file has unsafe permissions (0644)...
#   Fix: chmod 600 ~/.nexus/service-tokens

## Commit

git add \
  cmd/engx/cmd_install.go \
  cmd/engx/main.go \
  cmd/engxd/main.go \
  WORKFLOW-SESSION.md && \
git commit -m "feat(phase18-19): engxd system service install + token perm check + home bootstrap" && \
git tag v1.3.0-phase18 && \
git push origin main --tags
