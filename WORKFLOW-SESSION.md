# WORKFLOW-SESSION.md
# Session: nexus-fix-plan-executor-20260325
# Date: 2026-03-25

## What changed — Nexus fix: GAP-4 + GAP-5 (plan executor)

GAP-4: `plan.Run()` hardcoded `"RUNNING"` as the terminal success status. Added
`Plan.SuccessStatus string` field — defaults to `"RUNNING"` when empty, but
`engx build`, `engx check`, and future plan consumers can set `"BUILT"`,
`"PASSED"`, etc. without touching the executor.

GAP-5: `emitSpan()` used a raw `map[string]any` — the `duration_ms` field was
never emitted, and schema drift was invisible at compile time. Replaced with a
typed `SpanPayload` struct that mirrors `accord.PlanSpanDTO` exactly. Step start
time is now tracked in `Run()` and passed to `emitSpan()` so `duration_ms` is
always populated.

## New files
- (none)

## Modified files
- `internal/plan/plan.go`      — `Plan.SuccessStatus string` field added
- `internal/plan/executor.go`  — `SpanPayload` struct added; `Run()` tracks step
                                 duration; `emitSpan()` uses `SpanPayload` with
                                 `durationMS int64` parameter; `"RUNNING"` hardcode
                                 replaced with `p.SuccessStatus` defaulting to
                                 `"RUNNING"`
- `internal/plan/plan_test.go` — `TestRun_SuccessStatus` and `TestSpanPayload_Fields`
                                 added; `encoding/json` import added

## Apply

cd ~/workspace/projects/engx/services/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-fix-plan-executor-20260325.zip -d . && \
go build ./... && \
go test ./internal/plan/...

## Verify

go test ./internal/plan/...
# Expected: ok  github.com/Harshmaury/Nexus/internal/plan

## Commit

git add \
  internal/plan/plan.go \
  internal/plan/executor.go \
  internal/plan/plan_test.go \
  WORKFLOW-SESSION.md && \
git commit -m "fix(plan): GAP-4 SuccessStatus replaces hardcoded RUNNING; GAP-5 SpanPayload with duration_ms" && \
git tag v1.5.2-plan-executor && \
git push origin main --tags
