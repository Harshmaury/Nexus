// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_run.go
// engx run — primary user-facing command (ADR-040, ADR-043).
//
// User sees:   simple labels + tick/cross + outcome block
// Developer sees: PLAN_STEP spans in engx trace, structured per step
//
// v1.8.0: migrated to internal/plan model.
// runProject() is now BuildRunPlan() → plan.Run().
// All service call logic is unchanged — only structure added.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/Harshmaury/Nexus/internal/plan"
	"github.com/spf13/cobra"
)

func runCmd(socketPath, httpAddr *string) *cobra.Command {
	var timeout int
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "run <project>",
		Short: "Start a project and confirm it is running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			return runProject(*socketPath, *httpAddr, id, timeout, dryRun)
		},
	}
	cmd.Flags().IntVarP(&timeout, "timeout", "t", 60, "seconds to wait for running state")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print execution plan without running")
	return cmd
}

// runProject builds and executes (or prints) the run plan for project id.
func runProject(socketPath, httpAddr, id string, timeoutSecs int, dryRun bool) error {
	cfg := plan.RunConfig{NexusAddr: httpAddr}
	p := buildRunPlan(socketPath, httpAddr, id, timeoutSecs)
	if dryRun {
		plan.Print(p, os.Stdout)
		return nil
	}
	return plan.Run(context.Background(), p, os.Stdout, cfg)
}

// buildRunPlan constructs the four-step run plan.
// Construction is pure — no service calls made here.
func buildRunPlan(socketPath, httpAddr, id string, timeoutSecs int) *plan.Plan {
	return plan.Build("run:"+id, []*plan.Step{
		{
			Label: "Validating",
			Kind:  plan.KindValidate,
			Run:   stepValidate(httpAddr, id),
		},
		{
			Label: "Starting",
			Kind:  plan.KindExecute,
			Run:   stepStart(socketPath, id),
		},
		{
			Label: "Waiting",
			Kind:  plan.KindWait,
			Run:   stepWait(httpAddr, id, timeoutSecs),
		},
		{
			Label: "Health",
			Kind:  plan.KindObserve,
			Run:   stepHealth(httpAddr, id),
		},
	})
}

// stepValidate calls POST /system/validate (ADR-038).
// Fail-open if the validator is unavailable — returns Skip.
func stepValidate(httpAddr, id string) plan.StepFunc {
	return func(_ context.Context) plan.StepResult {
		validation, err := callValidate(httpAddr, id)
		if err != nil {
			return plan.StepResult{OK: true, Skip: true, Message: "validator unavailable"}
		}
		if !validation.allowed {
			msg := "policy violation"
			next := "engx doctor"
			for _, v := range validation.violations {
				if v.action == "deny" {
					msg = v.message
					next = fmt.Sprintf("engx register %s", id)
					break
				}
			}
			return plan.StepResult{
				OK: false,
				Err: &plan.UserError{
					What:     fmt.Sprintf("project %q blocked by policy", id),
					Where:    "nexus /system/validate",
					Why:      msg,
					NextStep: next,
				},
			}
		}
		warns := []string{}
		for _, v := range validation.violations {
			if v.action == "warn" {
				warns = append(warns, v.message)
			}
		}
		detail := "allowed"
		if len(warns) > 0 {
			detail = "allowed with warnings: " + strings.Join(warns, "; ")
		}
		return plan.StepResult{OK: true, Detail: detail}
	}
}

// stepStart sends CmdProjectStart to the daemon.
// Returns Skip if the project is already running.
func stepStart(socketPath, id string) plan.StepFunc {
	return func(_ context.Context) plan.StepResult {
		resp, err := sendCommand(socketPath, daemon.CmdProjectStart,
			daemon.ProjectStartParams{ProjectID: id})
		if err != nil {
			return plan.StepResult{
				OK: false,
				Err: &plan.UserError{
					What:     fmt.Sprintf("failed to start project %q", id),
					Where:    "nexus daemon",
					Why:      err.Error(),
					NextStep: "engxd &",
				},
			}
		}
		var r map[string]any
		_ = json.Unmarshal(resp.Data, &r)
		queued, _ := r["queued"].(float64)
		if int(queued) == 0 {
			return plan.StepResult{OK: true, Skip: true, Message: "already running"}
		}
		return plan.StepResult{OK: true, Detail: fmt.Sprintf("queued %d services", int(queued))}
	}
}

// stepWait polls until all project services reach running state or timeout.
func stepWait(httpAddr, id string, timeoutSecs int) plan.StepFunc {
	return func(_ context.Context) plan.StepResult {
		deadline := time.Now().Add(time.Duration(timeoutSecs) * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			running, total, err := projectServiceStates(httpAddr, id)
			if err != nil {
				continue
			}
			if running == total && total > 0 {
				return plan.StepResult{
					OK:     true,
					Detail: fmt.Sprintf("%d/%d services running", running, total),
					Message: fmt.Sprintf("%d/%d ✓", running, total),
				}
			}
		}
		running, total, _ := projectServiceStates(httpAddr, id)
		svcs := getProjectServices(httpAddr, id)
		for _, s := range svcs {
			if s.actualState != "running" && s.desiredState == "running" {
				why := fmt.Sprintf("did not reach running state within %ds", timeoutSecs)
				next := fmt.Sprintf("engx logs %s", s.id)
				if s.failCount > 0 {
					why = fmt.Sprintf("failed %d time(s) — likely bad binary or config", s.failCount)
				}
				// Fetch last crash message for a specific failure reason.
				if crashMsg := fetchLastCrashMessage(httpAddr, s.id); crashMsg != "" {
					why = crashMsg
				}
				return plan.StepResult{
					OK: false,
					Err: &plan.UserError{
						What:     fmt.Sprintf("%d/%d services started", running, total),
						Where:    s.id,
						Why:      why,
						NextStep: next,
					},
				}
			}
		}
		return plan.StepResult{
			OK: false,
			Err: &plan.UserError{
				What:     fmt.Sprintf("%d/%d services started", running, total),
				NextStep: "engx doctor",
			},
		}
	}
}

// stepHealth performs a lightweight health check on each project service.
// KindObserve — failure logs a warning but does not stop the plan.
func stepHealth(httpAddr, id string) plan.StepFunc {
	return func(_ context.Context) plan.StepResult {
		svcs := getProjectServices(httpAddr, id)
		healthy := 0
		for _, s := range svcs {
			if s.actualState == "running" {
				healthy++
			}
		}
		if healthy == 0 {
			return plan.StepResult{OK: false, Detail: "no services in running state"}
		}
		return plan.StepResult{
			OK:     true,
			Detail: fmt.Sprintf("%d services healthy", healthy),
		}
	}
}


// ── PROJECT STATUS HELPERS ────────────────────────────────────────────────────

type svcState struct {
	id           string
	actualState  string
	desiredState string
	failCount    int
}

func getProjectServices(httpAddr, projectID string) []svcState {
	var result struct {
		Data []struct {
			ID           string `json:"id"`
			ActualState  string `json:"actual_state"`
			DesiredState string `json:"desired_state"`
			FailCount    int    `json:"fail_count"`
		} `json:"data"`
	}
	if err := getJSON(fmt.Sprintf("%s/services?project=%s", httpAddr, projectID), &result); err != nil {
		return nil
	}
	out := make([]svcState, 0, len(result.Data))
	for _, s := range result.Data {
		out = append(out, svcState{
			id:           s.ID,
			actualState:  s.ActualState,
			desiredState: s.DesiredState,
			failCount:    s.FailCount,
		})
	}
	return out
}


// callValidate calls POST /system/validate (ADR-038).
type valResult struct {
	allowed    bool
	violations []struct{ ruleID, message, action string }
}

func callValidate(httpAddr, projectID string) (*valResult, error) {
	body, _ := json.Marshal(map[string]string{
		"project_id": projectID, "intent": "start",
	})
	resp, err := http.Post(httpAddr+"/system/validate",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Data struct {
			Allowed    bool `json:"allowed"`
			Violations []struct {
				RuleID  string `json:"rule_id"`
				Message string `json:"message"`
				Action  string `json:"action"`
			} `json:"violations"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	vr := &valResult{allowed: result.Data.Allowed}
	for _, v := range result.Data.Violations {
		vr.violations = append(vr.violations, struct{ ruleID, message, action string }{
			v.RuleID, v.Message, v.Action,
		})
	}
	return vr, nil
}
