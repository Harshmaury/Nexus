// @nexus-project: nexus
// @nexus-path: internal/plan/plan.go
// Package plan defines the command execution model for the engx CLI (ADR-043).
//
// A Plan is a named, ordered sequence of Steps constructed entirely before
// execution begins. It is the translation layer between a user command and
// the service calls that implement it.
//
// Construction never makes service calls. Execution never modifies the plan.
// These two invariants make plans inspectable (--dry-run) and testable.
package plan

import (
	"context"
	"time"
)

// Plan is a named, ordered sequence of steps.
// Constructed before execution — immutable during execution.
type Plan struct {
	ID    string  // root trace ID — propagated to all steps as X-Trace-ID
	Name  string  // human label, e.g. "run:atlas"
	Steps []*Step
}

// Step is one unit of work in a plan.
type Step struct {
	Label  string      // user-visible label, e.g. "Validating"
	Kind   StepKind    // classifies display and default retry behaviour
	Retry  RetryPolicy // how to handle transient failure
	Run    StepFunc    // the operation — called by the executor
}

// StepFunc is the operation a step performs.
// ctx carries the plan trace ID — steps must propagate it to service calls.
type StepFunc func(ctx context.Context) StepResult

// StepResult is the outcome of one step execution.
type StepResult struct {
	OK      bool       // true if the step succeeded or was skipped
	Skip    bool       // step skipped — not a failure (e.g. already running)
	Message string     // appended after the step label on the output line
	Detail  string     // developer-visible detail, emitted in the plan span
	Err     *UserError // set on failure — drives the outcome block
}

// StepKind classifies what a step does.
// Determines display (icon, label width) and executor stop-or-continue policy.
type StepKind int

const (
	KindValidate StepKind = iota // pre-flight check — fail-hard on deny
	KindExecute                  // service call with expected side effect
	KindWait                     // poll loop until condition met or timeout
	KindObserve                  // read-only check — fail-open (warn, continue)
)

// kindLabel returns the string name of a StepKind for span payloads.
func (k StepKind) String() string {
	switch k {
	case KindValidate:
		return "validate"
	case KindExecute:
		return "execute"
	case KindWait:
		return "wait"
	case KindObserve:
		return "observe"
	default:
		return "unknown"
	}
}

// RetryPolicy declares how a step handles transient failure.
// Zero value means no retry.
type RetryPolicy struct {
	MaxAttempts int           // 0 = no retry; 1 = one retry (2 total attempts)
	Backoff     time.Duration // wait between attempts
	// RetryOn returns true if the result warrants a retry.
	// nil defaults to: retry if !OK && Err == nil (transient, no UserError)
	RetryOn func(StepResult) bool
}

// UserError is a structured, actionable error shown directly to the user.
// Every failure block answers: what / where / why / next step.
type UserError struct {
	What     string // what happened
	Where    string // which component or step
	Why      string // root cause in plain language
	NextStep string // exactly what to type next
}

func (e *UserError) Error() string {
	if e == nil {
		return ""
	}
	return e.What
}

// Build constructs a Plan with a generated trace ID.
func Build(name string, steps []*Step) *Plan {
	return &Plan{
		ID:    newPlanID(),
		Name:  name,
		Steps: steps,
	}
}

// newPlanID generates a short random plan ID (8 hex chars).
func newPlanID() string {
	return randomHex(4)
}
