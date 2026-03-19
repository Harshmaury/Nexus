# WORKFLOW-SESSION.md
# Session: NX-phase17-automation-commands
# Date: 2026-03-19

## What changed — Nexus Phase 17 (ADR-025)

Automation layer for engx. Adds 9 new commands (status, sentinel, workflow,
trigger, guard, on, exec, ci, stream) plus --follow on logs. All new commands
support --output json for scripting and CI integration.

## New files

- cmd/engx/cmd_automation.go   — status, sentinel, workflow, trigger, guard, on, exec
- cmd/engx/cmd_ci.go           — ci check, ci wait, ci gate
- cmd/engx/cmd_follow.go       — logsFollowCmd (replaces logsCmd), eventsStreamCmd

## Modified files

- cmd/engx/main.go             — see MAIN_GO_PATCH.md for exact changes:
                                  imports: add bytes, os/exec, canon
                                  root.AddCommand: add 9 new commands
                                  root.AddCommand: replace logsCmd → logsFollowCmd
                                  remove logsCmd function (replaced by logsFollowCmd)
                                  fix getJSONWithToken: X-Service-Token → canon header

## Apply

cd ~/workspace/projects/apps/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-phase17-automation-commands-20260319-HHMM.zip -d . && \
go build ./cmd/engx/

## Manual step: apply MAIN_GO_PATCH.md

Read cmd/engx/MAIN_GO_PATCH.md and apply the 5 changes to cmd/engx/main.go.
The patch file has exact FIND/REPLACE instructions — no ambiguity.

## Verify

go build ./cmd/engx/ && echo "build ok"
./engx help | grep -E "ci|exec|guard|on|sentinel|status|stream|trigger|workflow"

# Functional checks (requires platform running):
./engx status
./engx status --output json | jq .health
./engx sentinel system
./engx sentinel explain
./engx workflow list
./engx trigger list
./engx ci check
./engx ci check --output json | jq .pass
./engx logs nexus-daemon --follow   # Ctrl-C to stop
./engx stream                        # Ctrl-C to stop

# Guard test (requires nexus project registered):
./engx guard nexus -- echo "platform is healthy"

# Automation test (requires forge running):
./engx on workspace.file.modified run-tests --filter extension=.go
./engx trigger list

## Commit

git add \
  cmd/engx/cmd_automation.go \
  cmd/engx/cmd_ci.go \
  cmd/engx/cmd_follow.go \
  cmd/engx/main.go \
  WORKFLOW-SESSION.md && \
git commit -m "feat(phase17): automation commands — status, sentinel, workflow, trigger, guard, ci, stream" && \
git tag v0.6.0-phase17 && \
git push origin main --tags

## New CLI surface after this phase

  engx status                       one-line platform health (--output json)
  engx sentinel system              full structured platform report
  engx sentinel explain             AI narrative reasoning (Sentinel phase 2)
  engx sentinel incidents           error-severity incidents only
  engx sentinel risk                deployment risk assessment
  engx workflow list                list Forge workflows
  engx workflow run <id>            execute a workflow
  engx workflow create --file f.json create from JSON definition
  engx trigger list                 list automation triggers
  engx trigger add <event> <wf>     register event→workflow trigger
  engx trigger remove <id>          remove a trigger
  engx guard <project> -- <cmd>     health-gated command execution
  engx on <event> <workflow>        shorthand trigger registration
  engx exec <project> <intent>      submit Forge intent directly
  engx ci check                     exits 0 if healthy (for CI gates)
  engx ci wait <project>            block until project ready
  engx ci gate                      strict health gate (services+guardian+sentinel)
  engx logs <id> --follow           real-time log tailing
  engx stream                       SSE event stream (Ctrl-C to stop)
