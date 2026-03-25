// @nexus-project: nexus
// @nexus-path: internal/plan/plan_test.go
package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func okStep(label string, kind StepKind) *Step {
	return &Step{
		Label: label,
		Kind:  kind,
		Run:   func(_ context.Context) StepResult { return StepResult{OK: true, Detail: "ok"} },
	}
}

func failStep(label string, kind StepKind, err *UserError) *Step {
	return &Step{
		Label: label,
		Kind:  kind,
		Run:   func(_ context.Context) StepResult { return StepResult{OK: false, Err: err} },
	}
}

func skipStep(label string) *Step {
	return &Step{
		Label: label,
		Kind:  KindExecute,
		Run:   func(_ context.Context) StepResult { return StepResult{OK: true, Skip: true, Message: "already running"} },
	}
}

func TestRun_AllSucceed(t *testing.T) {
	p := Build("test:all-ok", []*Step{
		okStep("Validating", KindValidate),
		okStep("Starting", KindExecute),
		okStep("Waiting", KindWait),
		okStep("Health", KindObserve),
	})
	var buf bytes.Buffer
	err := Run(context.Background(), p, &buf, RunConfig{})
	if err != nil {
		t.Errorf("Run() error = %v, want nil", err)
	}
	out := buf.String()
	if !strings.Contains(out, "RUNNING") {
		t.Errorf("output missing RUNNING: %s", out)
	}
}

// TestRun_SuccessStatus verifies that a non-empty SuccessStatus overrides "RUNNING".
// Needed for future plan consumers (engx build, engx check) whose outcome is not
// a running service. GAP-4 fix.
func TestRun_SuccessStatus(t *testing.T) {
	p := Build("test:custom-status", []*Step{
		okStep("Building", KindExecute),
	})
	p.SuccessStatus = "BUILT"
	var buf bytes.Buffer
	err := Run(context.Background(), p, &buf, RunConfig{})
	if err != nil {
		t.Errorf("Run() error = %v, want nil", err)
	}
	out := buf.String()
	if !strings.Contains(out, "BUILT") {
		t.Errorf("output missing custom SuccessStatus BUILT: %s", out)
	}
	if strings.Contains(out, "RUNNING") {
		t.Errorf("output should not contain RUNNING when SuccessStatus is set: %s", out)
	}
}

// TestSpanPayload_Fields verifies SpanPayload has all accord.PlanSpanDTO fields
// including duration_ms (was missing when map[string]any was used). GAP-5 fix.
func TestSpanPayload_Fields(t *testing.T) {
	span := SpanPayload{
		PlanID:     "abc123",
		PlanName:   "run:atlas",
		StepIndex:  2,
		StepLabel:  "Waiting",
		StepKind:   "wait",
		DurationMS: 1840,
		Outcome:    "success",
		Detail:     "3/3 services reached running state",
	}
	data, err := json.Marshal(span)
	if err != nil {
		t.Fatalf("marshal SpanPayload: %v", err)
	}
	s := string(data)
	for _, field := range []string{"plan_id", "plan_name", "step_index", "step_label", "step_kind", "duration_ms", "outcome", "detail"} {
		if !strings.Contains(s, field) {
			t.Errorf("SpanPayload JSON missing field %q: %s", field, s)
		}
	}
	if !strings.Contains(s, "1840") {
		t.Errorf("SpanPayload JSON missing duration_ms value 1840: %s", s)
	}
}

func TestRun_ValidateBlocks(t *testing.T) {
	p := Build("test:validate-block", []*Step{
		failStep("Validating", KindValidate, &UserError{
			What:     "project blocked",
			Where:    "nexus",
			Why:      "no services registered",
			NextStep: "engx register .",
		}),
		okStep("Starting", KindExecute),
	})
	var buf bytes.Buffer
	err := Run(context.Background(), p, &buf, RunConfig{})
	if err == nil {
		t.Fatal("Run() expected error from validate block, got nil")
	}
	out := buf.String()
	if !strings.Contains(out, "project blocked") {
		t.Errorf("output missing error What: %s", out)
	}
	if !strings.Contains(out, "engx register") {
		t.Errorf("output missing NextStep: %s", out)
	}
	if strings.Contains(out, "Starting") {
		t.Error("Starting step should not have executed after validate block")
	}
}

func TestRun_ExecuteFails(t *testing.T) {
	p := Build("test:execute-fail", []*Step{
		okStep("Validating", KindValidate),
		failStep("Starting", KindExecute, &UserError{
			What:     "daemon not running",
			NextStep: "engxd &",
		}),
		okStep("Waiting", KindWait),
	})
	var buf bytes.Buffer
	err := Run(context.Background(), p, &buf, RunConfig{})
	if err == nil {
		t.Fatal("Run() expected error, got nil")
	}
	out := buf.String()
	if strings.Contains(out, "Waiting") {
		t.Error("Waiting step should not execute after execute failure")
	}
}

func TestRun_ObserveFailContinues(t *testing.T) {
	p := Build("test:observe-continue", []*Step{
		okStep("Validating", KindValidate),
		okStep("Starting", KindExecute),
		{
			Label: "Health",
			Kind:  KindObserve,
			Run: func(_ context.Context) StepResult {
				return StepResult{OK: false, Detail: "health endpoint slow"}
			},
		},
	})
	var buf bytes.Buffer
	err := Run(context.Background(), p, &buf, RunConfig{})
	if err != nil {
		t.Errorf("Run() error = %v, want nil (observe should not stop plan)", err)
	}
	out := buf.String()
	if !strings.Contains(out, "RUNNING") {
		t.Errorf("output missing RUNNING: %s", out)
	}
}

func TestRun_SkipContinues(t *testing.T) {
	p := Build("test:skip", []*Step{
		okStep("Validating", KindValidate),
		skipStep("Starting"),
		okStep("Waiting", KindWait),
	})
	var buf bytes.Buffer
	err := Run(context.Background(), p, &buf, RunConfig{})
	if err != nil {
		t.Errorf("Run() error = %v, want nil", err)
	}
	out := buf.String()
	if !strings.Contains(out, "already running") {
		t.Errorf("output missing skip message: %s", out)
	}
}

func TestRun_RetryOnTransient(t *testing.T) {
	attempts := 0
	p := Build("test:retry", []*Step{
		{
			Label: "Connecting",
			Kind:  KindExecute,
			Retry: RetryPolicy{
				MaxAttempts: 2,
				Backoff:     time.Millisecond,
			},
			Run: func(_ context.Context) StepResult {
				attempts++
				if attempts < 3 {
					return StepResult{OK: false, Detail: "transient error"}
				}
				return StepResult{OK: true}
			},
		},
	})
	var buf bytes.Buffer
	err := Run(context.Background(), p, &buf, RunConfig{})
	if err != nil {
		t.Errorf("Run() error = %v, want nil after retries", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestPrint_DryRun(t *testing.T) {
	p := Build("run:atlas", []*Step{
		okStep("Validating", KindValidate),
		okStep("Starting", KindExecute),
		okStep("Waiting", KindWait),
		okStep("Health", KindObserve),
	})
	var buf bytes.Buffer
	Print(p, &buf)
	out := buf.String()

	for _, want := range []string{"run:atlas", "validate", "execute", "wait", "observe", "No changes made"} {
		if !strings.Contains(out, want) {
			t.Errorf("Print() output missing %q:\n%s", want, out)
		}
	}
}

func TestStepKindString(t *testing.T) {
	tests := []struct{ kind StepKind; want string }{
		{KindValidate, "validate"},
		{KindExecute, "execute"},
		{KindWait, "wait"},
		{KindObserve, "observe"},
	}
	for _, tc := range tests {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("StepKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
