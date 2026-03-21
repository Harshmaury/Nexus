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
		result := runStep(planCtx, step)
		printStepLine(w, step.Label, result)
		emitSpan(cfg, p, i, step, result)

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

	elapsed := time.Since(start)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  Status:   %s%s%s\n", colorGreen, "RUNNING", colorReset)
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
// Fails silently — never blocks user operations.
func emitSpan(cfg RunConfig, p *Plan, idx int, step *Step, r StepResult) {
	if cfg.NexusAddr == "" {
		return
	}
	outcome := "success"
	if r.Skip {
		outcome = "skipped"
	} else if !r.OK {
		outcome = "failure"
	}
	payload, err := json.Marshal(map[string]any{
		"plan_id":    p.ID,
		"plan_name":  p.Name,
		"step_index": idx,
		"step_label": step.Label,
		"step_kind":  step.Kind.String(),
		"outcome":    outcome,
		"detail":     r.Detail,
	})
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
