// @nexus-project: nexus
// @nexus-path: internal/plan/plan_test.go
package plan

import (
	"bytes"
	"context"
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
