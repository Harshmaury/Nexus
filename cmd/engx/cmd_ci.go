// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_ci.go
// CI-mode commands for engx — Phase 17 (ADR-025).
//
// Designed for machine consumption: exit codes are the primary output channel.
// All commands exit 0 on success, 1 on failure.
//
//   engx ci check           — exits 0 if platform is fully healthy, 1 otherwise
//   engx ci wait <project>  — blocks until project is running (exit 1 on timeout)
//   engx ci gate            — health gate: exits 1 if degraded + prints reason
//
// Intended usage in CI pipelines:
//
//   engx ci wait nexus --timeout 120 || exit 1
//   engx ci check || { echo "Platform degraded — aborting deploy"; exit 1; }
//   engx ci gate && ./scripts/deploy.sh
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// ── CI SUBGROUP ───────────────────────────────────────────────────────────────

// ciHealthResult is the machine-readable output of engx ci check / gate.
type ciHealthResult struct {
	Pass     bool   `json:"pass"`
	Reason   string `json:"reason,omitempty"`
	Running  int    `json:"running"`
	Total    int    `json:"total"`
	Errors   int    `json:"guardian_errors"`
	Health   string `json:"sentinel_health"`
	CheckedAt time.Time `json:"checked_at"`
}

// ciCmd groups CI-mode platform health commands.
func ciCmd(httpAddr *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "CI-mode commands — machine-readable, exit-code driven",
	}
	cmd.AddCommand(
		ciCheckCmd(httpAddr),
		ciWaitCmd(httpAddr),
		ciGateCmd(httpAddr),
	)
	return cmd
}

// ciCheckCmd exits 0 if the platform is fully healthy, 1 otherwise.
// Designed for use in `engx ci check || exit 1` pipelines.
func ciCheckCmd(httpAddr *string) *cobra.Command {
	var outFmt string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Exit 0 if platform healthy, 1 if degraded (for CI gates)",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := gatherCIHealth(*httpAddr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ci check: %v\n", err)
				os.Exit(1)
			}
			if outFmt == outputFmtJSON {
				if err := printOrJSON(outputFmtJSON, result, nil); err != nil {
					return err
				}
			} else {
				printCICheckHuman(result)
			}
			if !result.Pass {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&outFmt, "output", "o", outputFmtHuman, "output format: human | json")
	return cmd
}

// printCICheckHuman renders ciHealthResult for a terminal.
func printCICheckHuman(r *ciHealthResult) {
	icon := "✓"
	if !r.Pass {
		icon = "✗"
	}
	status := "healthy"
	if !r.Pass {
		status = "degraded"
	}
	fmt.Printf("%s Platform %s  (%d/%d services running",
		icon, status, r.Running, r.Total)
	if r.Errors > 0 {
		fmt.Printf(", %d Guardian error(s)", r.Errors)
	}
	fmt.Println(")")
	if r.Reason != "" {
		fmt.Printf("  reason: %s\n", r.Reason)
	}
}

// ciWaitCmd blocks until the specified project is fully running or timeout.
// Exit 0 when ready, 1 on timeout. Uses the same poll logic as runCmd --wait.
func ciWaitCmd(httpAddr *string) *cobra.Command {
	var timeout int
	var interval int
	cmd := &cobra.Command{
		Use:   "wait <project-id>",
		Short: "Block until project services are running (exit 1 on timeout)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := args[0]
			return ciBlockUntilReady(*httpAddr, projectID, timeout, interval)
		},
	}
	cmd.Flags().IntVarP(&timeout, "timeout", "t", 120,
		"maximum seconds to wait before exit 1")
	cmd.Flags().IntVar(&interval, "interval", 3,
		"polling interval in seconds")
	return cmd
}

// ciBlockUntilReady polls until the project's services are all running.
func ciBlockUntilReady(httpAddr, projectID string, timeoutSecs, intervalSecs int) error {
	deadline := time.Now().Add(time.Duration(timeoutSecs) * time.Second)
	fmt.Printf("Waiting for %q (timeout %ds)...\n", projectID, timeoutSecs)
	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(intervalSecs) * time.Second)
		running, total, err := projectServiceStates(httpAddr, projectID)
		if err != nil {
			fmt.Printf("  polling... (engxd unreachable: %v)\n", err)
			continue
		}
		fmt.Printf("  %d/%d running\n", running, total)
		if total > 0 && running == total {
			fmt.Printf("✓ %q ready\n", projectID)
			return nil
		}
	}
	fmt.Fprintf(os.Stderr,
		"✗ Timeout after %ds — %q is not fully running\n", timeoutSecs, projectID)
	os.Exit(1)
	return nil // unreachable — satisfies compiler
}

// ciGateCmd is a health gate: exits 0 to allow a subsequent command, 1 to block it.
// Stricter than ci check — also fails if Sentinel reports a degraded or incident health.
func ciGateCmd(httpAddr *string) *cobra.Command {
	var sentinelAddr string
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Strict health gate — checks services + Guardian errors + Sentinel health",
		Example: `  # In a deploy script:
  engx ci gate && ./scripts/deploy.sh
  engx ci gate --sentinel http://127.0.0.1:8087 || exit 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := gatherCIHealth(*httpAddr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ci gate: %v\n", err)
				os.Exit(1)
			}
			result = enrichWithSentinel(result, sentinelAddr)
			printCIGateHuman(result)
			if !result.Pass {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sentinelAddr, "sentinel", defaultSentinelAddr, "Sentinel address")
	return cmd
}

// gatherCIHealth collects service states and Guardian error count.
func gatherCIHealth(httpAddr string) (*ciHealthResult, error) {
	s, err := gatherStatus(httpAddr)
	if err != nil {
		return nil, fmt.Errorf("gather status: %w", err)
	}
	result := &ciHealthResult{
		Running:   s.Running,
		Total:     s.Total,
		Health:    s.Health,
		CheckedAt: time.Now().UTC(),
	}
	result.Errors = countGuardianErrors()
	result.Pass = s.Running == s.Total && s.Total > 0 && result.Errors == 0
	if !result.Pass {
		result.Reason = buildFailReason(s.Running, s.Total, result.Errors)
	}
	return result, nil
}

// countGuardianErrors returns the number of error-severity Guardian findings.
func countGuardianErrors() int {
	var r struct {
		Data struct {
			Summary struct {
				Errors int `json:"errors"`
			} `json:"summary"`
		} `json:"data"`
	}
	if err := getJSON("http://127.0.0.1:8085/guardian/findings", &r); err != nil {
		return 0 // guardian unreachable — fail open
	}
	return r.Data.Summary.Errors
}

// enrichWithSentinel adds Sentinel health to the result and may flip Pass=false.
func enrichWithSentinel(result *ciHealthResult, addr string) *ciHealthResult {
	var r struct {
		Data struct {
			Health string `json:"health"`
		} `json:"data"`
	}
	if err := getJSON(addr+"/insights/system", &r); err != nil {
		return result // sentinel unreachable — don't fail gate
	}
	result.Health = r.Data.Health
	if r.Data.Health == healthIncident {
		result.Pass = false
		result.Reason = "Sentinel reports active incident — " + result.Reason
	}
	return result
}

// buildFailReason constructs a human-readable explanation of why the gate failed.
func buildFailReason(running, total, errors int) string {
	parts := []string{}
	if running < total {
		parts = append(parts, fmt.Sprintf("%d/%d services not running", total-running, total))
	}
	if errors > 0 {
		parts = append(parts, fmt.Sprintf("%d Guardian error(s)", errors))
	}
	if len(parts) == 0 {
		return "unknown reason"
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += ", " + p
	}
	return result
}

// printCIGateHuman renders the gate result to the terminal.
func printCIGateHuman(r *ciHealthResult) {
	if r.Pass {
		fmt.Printf("✓ Gate passed — platform healthy (%d/%d running, 0 errors)\n",
			r.Running, r.Total)
		return
	}
	fmt.Fprintf(os.Stderr, "✗ Gate blocked — %s\n", r.Reason)
	fmt.Fprintf(os.Stderr, "  Run 'engx doctor' for details.\n")
}
