// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_enforce.go
// stepEnforce — Arbiter architectural gate for engx run (ADR-047).
// Inserted between Validating and Starting in buildRunPlan.
// --skip-enforce bypasses the gate and emits a SYSTEM_ALERT to Nexus.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	arbiter "github.com/Harshmaury/Arbiter/api"
	"github.com/Harshmaury/Nexus/internal/plan"
)

// stepEnforce runs the Arbiter execution gate (ADR-047 §3.2).
func stepEnforce(httpAddr, projectID string, skipEnforce bool) plan.StepFunc {
	return func(_ context.Context) plan.StepResult {
		if skipEnforce {
			arbiter.SkipEnforceAlert(httpAddr, "", projectID)
			return plan.StepResult{
				OK:      true,
				Skip:    true,
				Message: "bypassed (audit logged)",
			}
		}

		projectDir := resolveProjectDir(projectID)
		if projectDir == "" {
			return plan.StepResult{
				OK:      true,
				Skip:    true,
				Message: "skipped (project dir not found)",
			}
		}

		report, err := arbiter.VerifyExecution(httpAddr, "", projectDir)
		if err != nil {
			// Fail-open — never block execution due to Arbiter error.
			return plan.StepResult{
				OK:      true,
				Skip:    true,
				Message: "skipped (arbiter unavailable)",
				Detail:  err.Error(),
			}
		}

		if report.OK() {
			return plan.StepResult{
				OK:      true,
				Message: fmt.Sprintf("✓ (%d rules)", len(report.Passed)),
			}
		}

		return plan.StepResult{
			OK:     false,
			Detail: arbiter.FormatReport(report),
			Err: &plan.UserError{
				What:     "Arbiter enforcement gate failed",
				Where:    "Enforcing",
				Why:      fmt.Sprintf("%d architectural violation(s) detected", len(report.Violations)),
				NextStep: "resolve violations and re-run, or use --skip-enforce to bypass (audited)",
			},
		}
	}
}

// resolveProjectDir returns the local path for a registered project.
// Checks the conventional workspace path: ~/workspace/projects/engx/services/<id>
func resolveProjectDir(projectID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(home, "workspace", "projects", "engx", "services", projectID)
	if _, err := os.Stat(filepath.Join(candidate, ".nexus.yaml")); err == nil {
		return candidate
	}
	return ""
}
