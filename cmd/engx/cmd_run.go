// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_run.go
// engx run — the primary user-facing command.
// Proposal: outcome-centric interface. User runs one command and gets:
//   - validation before start
//   - real-time progress
//   - clear outcome (running / failed / why / next step)
// No internal service knowledge required.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/spf13/cobra"
)

func runCmd(socketPath, httpAddr *string) *cobra.Command {
	var timeout int
	cmd := &cobra.Command{
		Use:   "run <project>",
		Short: "Start a project and confirm it is running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			return runProject(*socketPath, *httpAddr, id, timeout)
		},
	}
	cmd.Flags().IntVarP(&timeout, "timeout", "t", 60, "seconds to wait for running state")
	return cmd
}

// runProject is the full outcome-centric run flow:
//   1. Validate — check project is safe to start
//   2. Start    — queue services
//   3. Monitor  — wait and report progress
//   4. Outcome  — clear success or failure with next step
func runProject(socketPath, httpAddr, id string, timeoutSecs int) error {
	fmt.Printf("\nProject: %s\n\n", id)

	// ── Step 1: Validate ──────────────────────────────────────────────────
	fmt.Print("  Validating... ")
	validation, err := callValidate(httpAddr, id)
	if err != nil {
		// Validation endpoint unavailable — proceed with warning, do not block.
		fmt.Println("skipped (validator unavailable)")
	} else if !validation.allowed {
		fmt.Println("\033[31mblocked\033[0m")
		fmt.Println()
		for _, v := range validation.violations {
			if v.action == "deny" {
				fmt.Printf("  What:      project cannot start\n")
				fmt.Printf("  Why:       %s\n", v.message)
				fmt.Printf("  Next step: engx register %s\n\n", id)
			}
		}
		return fmt.Errorf("project %q blocked by policy", id)
	} else {
		fmt.Println("\033[32m✓\033[0m")
		for _, v := range validation.violations {
			if v.action == "warn" {
				fmt.Printf("  ! %s\n", v.message)
			}
		}
	}

	// ── Step 2: Start ─────────────────────────────────────────────────────
	fmt.Print("  Starting...  ")
	resp, err := sendCommand(socketPath, daemon.CmdProjectStart,
		daemon.ProjectStartParams{ProjectID: id})
	if err != nil {
		fmt.Println("\033[31m✗\033[0m")
		fmt.Println()
		printOutcome("start", id, err.Error(), "")
		return fmt.Errorf("start failed")
	}
	var r map[string]any
	_ = json.Unmarshal(resp.Data, &r)
	queued, _ := r["queued"].(float64)
	if int(queued) == 0 {
		fmt.Println("\033[32malready running\033[0m")
		fmt.Printf("\n  Status: \033[32mRUNNING\033[0m\n\n")
		return nil
	}
	fmt.Println("\033[32m✓\033[0m")

	// ── Step 3: Monitor ───────────────────────────────────────────────────
	fmt.Printf("  Waiting...   ")
	deadline := time.Now().Add(time.Duration(timeoutSecs) * time.Second)
	last := 0
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		running, total, err := projectServiceStates(httpAddr, id)
		if err != nil {
			continue
		}
		// Print progress dots without newline
		for i := last; i < running; i++ {
			fmt.Print("·")
		}
		last = running
		if running == total && total > 0 {
			fmt.Println(" \033[32m✓\033[0m")
			fmt.Println()
			fmt.Printf("  Status:     \033[32mRUNNING\033[0m\n")
			fmt.Printf("  Services:   %d/%d running\n\n", running, total)
			return nil
		}
	}

	// ── Step 4: Timeout outcome ───────────────────────────────────────────
	fmt.Println(" \033[31m✗\033[0m")
	running, total, _ := projectServiceStates(httpAddr, id)
	fmt.Println()
	fmt.Printf("  Status:     \033[31mFAILED\033[0m\n\n")

	// Collect failure details from services
	var failedSvc string
	svcs := getProjectServices(httpAddr, id)
	for _, s := range svcs {
		if s.actualState != "running" && s.desiredState == "running" {
			failSvc := s.id
			fmt.Printf("  What:       %d/%d services started\n", running, total)
			if s.failCount > 0 {
				fmt.Printf("  Where:      %s\n", failSvc)
				fmt.Printf("  Why:        service failed %d time(s) — likely bad binary or config\n", s.failCount)
			} else {
				fmt.Printf("  Where:      %s (state: %s)\n", failSvc, s.actualState)
				fmt.Printf("  Why:        service did not reach running state within %ds\n", timeoutSecs)
			}
			failSvc = strings.TrimSuffix(failSvc, "-daemon")
			fmt.Printf("  Next step:  engx logs %s\n\n", s.id)
			failedSvc = s.id
			break
		}
	}
	if failedSvc == "" {
		fmt.Printf("  What:       %d/%d services started\n", running, total)
		fmt.Printf("  Next step:  engx doctor\n\n")
	}

	return fmt.Errorf("project %q did not reach running state", id)
}

// printOutcome prints a structured 4-field outcome block.
func printOutcome(step, project, cause, nextStep string) {
	if nextStep == "" {
		nextStep = fmt.Sprintf("engx doctor")
	}
	fmt.Printf("  Status:     \033[31mFAILED\033[0m\n")
	fmt.Printf("  Where:      %s (%s)\n", project, step)
	fmt.Printf("  Why:        %s\n", cause)
	fmt.Printf("  Next step:  %s\n\n", nextStep)
}

// ── PROJECT STATUS ────────────────────────────────────────────────────────────

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
	violations []struct {
		ruleID  string
		message string
		action  string
	}
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
		vr.violations = append(vr.violations, struct {
			ruleID, message, action string
		}{v.RuleID, v.Message, v.Action})
	}
	return vr, nil
}
