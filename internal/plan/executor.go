// @nexus-project: nexus
// @nexus-path: internal/plan/executor.go
// Executor runs a Plan step by step, printing progress and emitting spans.
// The user sees labels and tick/cross outcomes.
// Developers see structured PLAN_STEP events in Nexus via engx trace.
package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	canon "github.com/Harshmaury/Canon/identity"
)

const (
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorReset  = "\033[0m"
	checkMark   = "✓"
	crossMark   = "✗"
	labelWidth  = 16
)

// SpanPayload is the structured payload for PLAN_STEP events.
// Mirrors accord.PlanSpanDTO exactly — kept local to avoid adding Accord
// as a direct Nexus dependency until the module graph is consolidated.
// If Accord is added to go.mod, replace this with accord.PlanSpanDTO.
type SpanPayload struct {
	PlanID     string `json:"plan_id"`
	PlanName   string `json:"plan_name"`
	StepIndex  int    `json:"step_index"`
	StepLabel  string `json:"step_label"`
	StepKind   string `json:"step_kind"`
	DurationMS int64  `json:"duration_ms"`
	Outcome    string `json:"outcome"`          // "success" | "failure" | "skipped"
	Detail     string `json:"detail,omitempty"`
}

// RunConfig holds executor dependencies.
type RunConfig struct {
	// NexusAddr is the Nexus HTTP address for span emission.
	// Empty or unreachable — spans are silently dropped (fail-open).
	NexusAddr string
	// ServiceToken for span emission requests (ADR-008).
	ServiceToken string
}

// Run executes the plan, writing progress to w.
// Returns nil if all steps succeed or skip.
// Returns the first UserError on hard stop.
// KindObserve failures log a warning and continue — never stop the plan.
func Run(ctx context.Context, p *Plan, w io.Writer, cfg RunConfig) error {
	planCtx := contextWithTraceID(ctx, p.ID)
	start := time.Now()

	fmt.Fprintf(w, "\n  Plan: %s\n\n", p.Name)

	for i, step := range p.Steps {
		stepStart := time.Now()
		result := runStep(planCtx, step)
		durationMS := time.Since(stepStart).Milliseconds()
		printStepLine(w, step.Label, result)
		emitSpan(cfg, p, i, step, result, durationMS)

		if result.Skip {
			continue
		}
		if !result.OK {
			if step.Kind == KindObserve {
				fmt.Fprintf(w, "  ! %s — continuing\n", result.Detail)
				continue
			}
			fmt.Fprintln(w)
			printOutcomeBlock(w, result.Err)
			return result.Err
		}
	}

	// GAP-4: use plan's SuccessStatus if set, default to "RUNNING".
	status := p.SuccessStatus
	if status == "" {
		status = "RUNNING"
	}
	elapsed := time.Since(start)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  Status:   %s%s%s\n", colorGreen, status, colorReset)
	fmt.Fprintf(w, "  Elapsed:  %.1fs\n\n", elapsed.Seconds())
	return nil
}

// Print writes the dry-run plan listing to w without executing anything.
func Print(p *Plan, w io.Writer) {
	fmt.Fprintf(w, "\n  Plan: %s\n", p.Name)
	fmt.Fprintf(w, "  %s\n", strings.Repeat("─", 44))
	for i, step := range p.Steps {
		fmt.Fprintf(w, "  %d  %-10s %s\n", i+1, step.Kind.String(), step.Label)
	}
	fmt.Fprintf(w, "  %s\n", strings.Repeat("─", 44))
	fmt.Fprintf(w, "  No changes made.\n\n")
}

// runStep executes one step, applying the retry policy.
func runStep(ctx context.Context, step *Step) StepResult {
	policy := step.Retry
	maxAttempts := policy.MaxAttempts + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var result StepResult
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 && policy.Backoff > 0 {
			time.Sleep(policy.Backoff)
		}
		result = step.Run(ctx)
		if result.OK || result.Skip {
			return result
		}
		if result.Err != nil {
			return result
		}
		if policy.RetryOn != nil && !policy.RetryOn(result) {
			return result
		}
	}
	return result
}

// printStepLine writes one step's progress line to w.
func printStepLine(w io.Writer, label string, r StepResult) {
	padded := fmt.Sprintf("%-*s", labelWidth, label)
	switch {
	case r.Skip:
		msg := r.Message
		if msg == "" {
			msg = "skipped"
		}
		fmt.Fprintf(w, "  %s  %s%s%s\n", padded, colorYellow, msg, colorReset)
	case r.OK:
		msg := r.Message
		if msg == "" {
			msg = checkMark
		}
		fmt.Fprintf(w, "  %s  %s%s%s\n", padded, colorGreen, msg, colorReset)
	default:
		fmt.Fprintf(w, "  %s  %s%s%s\n", padded, colorRed, crossMark, colorReset)
	}
}

// printOutcomeBlock writes the structured failure block to w.
func printOutcomeBlock(w io.Writer, err *UserError) {
	if err == nil {
		fmt.Fprintf(w, "  Status:    %sFAILED%s\n\n", colorRed, colorReset)
		return
	}
	fmt.Fprintf(w, "  Status:    %sFAILED%s\n\n", colorRed, colorReset)
	fmt.Fprintf(w, "  What:      %s\n", err.What)
	if err.Where != "" {
		fmt.Fprintf(w, "  Where:     %s\n", err.Where)
	}
	if err.Why != "" {
		fmt.Fprintf(w, "  Why:       %s\n", err.Why)
	}
	if err.NextStep != "" {
		fmt.Fprintf(w, "  Next step: %s\n", err.NextStep)
	}
	fmt.Fprintln(w)
}

// emitSpan fires a PLAN_STEP event to Nexus for developer observability.
// Uses SpanPayload (mirrors accord.PlanSpanDTO) — schema-safe, includes duration_ms.
// Fails silently — never blocks user operations.
func emitSpan(cfg RunConfig, p *Plan, idx int, step *Step, r StepResult, durationMS int64) {
	if cfg.NexusAddr == "" {
		return
	}
	outcome := "success"
	if r.Skip {
		outcome = "skipped"
	} else if !r.OK {
		outcome = "failure"
	}
	span := SpanPayload{
		PlanID:     p.ID,
		PlanName:   p.Name,
		StepIndex:  idx,
		StepLabel:  step.Label,
		StepKind:   step.Kind.String(),
		DurationMS: durationMS,
		Outcome:    outcome,
		Detail:     r.Detail,
	}
	payload, err := json.Marshal(span)
	if err != nil {
		return
	}
	body := fmt.Sprintf(
		`{"type":"PLAN_STEP","source":"engx","component":"engx","outcome":%q,"payload":%s}`,
		outcome, string(payload),
	)
	go fireSpan(cfg.NexusAddr, cfg.ServiceToken, p.ID, body)
}

// fireSpan sends the span event in a goroutine — non-blocking.
func fireSpan(nexusAddr, token, traceID, body string) {
	req, err := http.NewRequest(http.MethodPost, nexusAddr+"/events",
		strings.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(canon.ServiceTokenHeader, token)
	}
	if traceID != "" {
		req.Header.Set(canon.TraceIDHeader, traceID)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}
