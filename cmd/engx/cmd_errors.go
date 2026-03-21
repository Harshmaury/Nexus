// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_errors.go
// Structured error formatting for outcome-centric UX (UX Proposal).
// Every user-facing error answers: what / where / why / next step.
package main

import (
	"fmt"
	"strings"
)

// UserError is a structured error with actionable context.
// Use this instead of fmt.Errorf for errors shown directly to users.
type UserError struct {
	What     string // what happened (brief)
	Where    string // which component or step
	Why      string // root cause in plain language
	NextStep string // exactly what to type next
}

func (e *UserError) Error() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n  What:      %s\n", e.What))
	if e.Where != "" {
		sb.WriteString(fmt.Sprintf("  Where:     %s\n", e.Where))
	}
	if e.Why != "" {
		sb.WriteString(fmt.Sprintf("  Why:       %s\n", e.Why))
	}
	if e.NextStep != "" {
		sb.WriteString(fmt.Sprintf("  Next step: %s\n", e.NextStep))
	}
	return sb.String()
}

// Common user errors — use these instead of raw fmt.Errorf.

func errDaemonDown(httpAddr string) *UserError {
	return &UserError{
		What:     "platform daemon is not running",
		Where:    httpAddr,
		Why:      "engxd process is not started or crashed",
		NextStep: "engxd &",
	}
}

func errProjectNotFound(id string) *UserError {
	return &UserError{
		What:     fmt.Sprintf("project %q is not registered", id),
		Where:    "nexus registry",
		Why:      "project has not been registered with the platform",
		NextStep: fmt.Sprintf("engx register ~/workspace/projects/engx/services/%s", id),
	}
}

func errServiceMaintenance(serviceID string) *UserError {
	return &UserError{
		What:     fmt.Sprintf("service %q is stuck in maintenance", serviceID),
		Where:    serviceID,
		Why:      "service exceeded restart threshold — automatically paused",
		NextStep: fmt.Sprintf("engx services reset %s", serviceID),
	}
}

func errBuildFailed(project, reason string) *UserError {
	return &UserError{
		What:     fmt.Sprintf("build failed for %q", project),
		Where:    "forge → build",
		Why:      reason,
		NextStep: fmt.Sprintf("engx logs %s-daemon", project),
	}
}
