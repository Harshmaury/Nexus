// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_doctor.go
// Doctor command — aggregates health signals from all platform observers
// and prints a single human-readable diagnosis. Read-only, no writes.
package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func doctorCmd(httpAddr *string) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose platform health — aggregates all observer signals",
		RunE: func(cmd *cobra.Command, args []string) error {
			d := &doctorReport{addr: *httpAddr}
			d.collect()
			d.print()
			return nil
		},
	}
}

// doctorReport holds all collected signals for one diagnostic run.
type doctorReport struct {
	addr     string
	services []doctorService
	agents   []doctorAgent
	guardian doctorGuardian
	sentinel doctorSentinel
	metrics  doctorMetrics
	fsChecks []doctorFSCheck
	errors   []string
}

type doctorService struct {
	Name         string `json:"name"`
	DesiredState string `json:"desired_state"`
	ActualState  string `json:"actual_state"`
	FailCount    int    `json:"fail_count"`
}

type doctorAgent struct {
	ID       string `json:"id"`
	Online   bool   `json:"online"`
	LastSeen string `json:"last_seen"`
}

type doctorGuardian struct {
	Total    int
	Warnings int
	Errors   int
	Findings []struct {
		RuleID   string `json:"rule_id"`
		Target   string `json:"target"`
		Message  string `json:"message"`
		Severity string `json:"severity"`
	}
}

type doctorSentinel struct {
	Health  string `json:"health"`
	Summary string `json:"summary"`
}

type doctorMetrics struct {
	UptimeSeconds   float64 `json:"uptime_seconds"`
	ServicesRunning int64   `json:"services_running"`
	RecentCrashes   int     `json:"recent_crashes"`
	TotalExecutions int     `json:"total_executions"`
}

// collect fetches all diagnostic signals.
func (d *doctorReport) collect() {
	fetchServices(d)
	fetchAgents(d)
	fetchGuardian(d)
	fetchSentinel(d)
	fetchMetrics(d)
	collectFS(d) // filesystem checks from cmd_doctor_fs.go
}

func fetchServices(d *doctorReport) {
	var result struct {
		Data []doctorService `json:"data"`
	}
	if err := getJSON(d.addr+"/services", &result); err != nil {
		d.errors = append(d.errors, "services: "+err.Error())
		return
	}
	d.services = result.Data
}

func fetchAgents(d *doctorReport) {
	var result struct {
		Data []doctorAgent `json:"data"`
	}
	if err := getJSON(d.addr+"/agents", &result); err != nil {
		d.errors = append(d.errors, "agents: "+err.Error())
	}
	d.agents = result.Data
}

func fetchGuardian(d *doctorReport) {
	var result struct {
		Data struct {
			Summary struct {
				Total    int `json:"total"`
				Warnings int `json:"warnings"`
				Errors   int `json:"errors"`
			} `json:"summary"`
			Findings []struct {
				RuleID   string `json:"rule_id"`
				Target   string `json:"target"`
				Message  string `json:"message"`
				Severity string `json:"severity"`
			} `json:"findings"`
		} `json:"data"`
	}
	if err := getJSON("http://127.0.0.1:8085/guardian/findings", &result); err != nil {
		d.errors = append(d.errors, "guardian: "+err.Error())
		return
	}
	d.guardian.Total = result.Data.Summary.Total
	d.guardian.Warnings = result.Data.Summary.Warnings
	d.guardian.Errors = result.Data.Summary.Errors
	d.guardian.Findings = result.Data.Findings
}

func fetchSentinel(d *doctorReport) {
	var result struct {
		Data doctorSentinel `json:"data"`
	}
	if err := getJSON("http://127.0.0.1:8087/insights/system", &result); err != nil {
		d.errors = append(d.errors, "sentinel: "+err.Error())
		return
	}
	d.sentinel = result.Data
}

func fetchMetrics(d *doctorReport) {
	var result struct {
		Data struct {
			Nexus struct {
				UptimeSeconds   float64 `json:"uptime_seconds"`
				ServicesRunning int64   `json:"services_running"`
			} `json:"nexus"`
			Events struct {
				RecentCrashes int `json:"recent_crashes"`
			} `json:"events"`
			Forge struct {
				TotalExecutions int `json:"total_executions"`
			} `json:"forge"`
		} `json:"data"`
	}
	if err := getJSON("http://127.0.0.1:8083/metrics/snapshot", &result); err != nil {
		d.errors = append(d.errors, "metrics: "+err.Error())
		return
	}
	d.metrics.UptimeSeconds = result.Data.Nexus.UptimeSeconds
	d.metrics.ServicesRunning = result.Data.Nexus.ServicesRunning
	d.metrics.RecentCrashes = result.Data.Events.RecentCrashes
	d.metrics.TotalExecutions = result.Data.Forge.TotalExecutions
}

// print renders the diagnostic report to stdout.
func (d *doctorReport) print() {
	fmt.Println()
	fmt.Println("  engx doctor — platform diagnosis")
	fmt.Println("  " + strings.Repeat("─", 48))
	printDaemon(d)
	printAgents(d)
	printServices(d)
	printGuardian(d)
	printSentinel(d)
	printForgeDoctor(d)
	printFS(d) // filesystem checks from cmd_doctor_fs.go
	printFetchErrors(d)
	fmt.Println()
	printSuggestions(d)
	fmt.Println()
}

func printDaemon(d *doctorReport) {
	if d.metrics.UptimeSeconds > 0 {
		fmt.Printf("  ✓ engxd running       uptime %s\n", formatUptime(d.metrics.UptimeSeconds))
	} else {
		fmt.Println("  ○ engxd metrics unavailable")
	}
}

func printAgents(d *doctorReport) {
	if len(d.agents) == 0 {
		fmt.Println("  ○ engxa               not connected")
		return
	}
	for _, a := range d.agents {
		if a.Online {
			fmt.Printf("  ✓ engxa               online (id=%s last=%s)\n", a.ID, a.LastSeen)
		} else {
			fmt.Printf("  ○ engxa               offline (id=%s)\n", a.ID)
		}
	}
}

func printServices(d *doctorReport) {
	total, running, maint := 0, 0, 0
	for _, s := range d.services {
		if s.DesiredState == "stopped" {
			continue
		}
		total++
		switch s.ActualState {
		case "running":
			running++
		case "maintenance":
			maint++
		}
	}
	if total == 0 {
		fmt.Println("  ○ services            none registered")
		return
	}
	icon := "✓"
	if running < total {
		icon = "○"
	}
	fmt.Printf("  %s services            %d/%d running", icon, running, total)
	if maint > 0 {
		fmt.Printf(" (%d in maintenance)", maint)
	}
	fmt.Println()
	for _, s := range d.services {
		if s.DesiredState == "stopped" || s.ActualState == "running" {
			continue
		}
		fmt.Printf("    ✗ %-22s actual=%-12s fails=%d\n",
			s.Name, s.ActualState, s.FailCount)
	}
}

func printGuardian(d *doctorReport) {
	if d.guardian.Total == 0 {
		fmt.Println("  ✓ guardian            no findings")
		return
	}
	fmt.Printf("  ○ guardian            %d finding(s) — %d warnings %d errors\n",
		d.guardian.Total, d.guardian.Warnings, d.guardian.Errors)
	for _, f := range d.guardian.Findings {
		fmt.Printf("    [%s] %s: %s\n", f.RuleID, f.Target, truncate(f.Message, 60))
	}
}

func printSentinel(d *doctorReport) {
	if d.sentinel.Health == "" {
		fmt.Println("  ○ sentinel            unavailable")
		return
	}
	icon := "✓"
	if d.sentinel.Health != "healthy" {
		icon = "○"
	}
	fmt.Printf("  %s sentinel            %s — %s\n",
		icon, d.sentinel.Health, truncate(d.sentinel.Summary, 50))
}

func printForgeDoctor(d *doctorReport) {
	if d.metrics.TotalExecutions == 0 {
		fmt.Println("  ✓ forge               no executions yet")
		return
	}
	fmt.Printf("  ✓ forge               %d execution(s) total\n", d.metrics.TotalExecutions)
	if d.metrics.RecentCrashes > 0 {
		fmt.Printf("  ○ recent crashes      %d in last 10 minutes\n", d.metrics.RecentCrashes)
	}
}

func printFetchErrors(d *doctorReport) {
	for _, e := range d.errors {
		fmt.Printf("  ! fetch error: %s\n", e)
	}
}

func printSuggestions(d *doctorReport) {
	suggestions := buildSuggestions(d)
	sentinelBad := d.sentinel.Health == "incident" || d.sentinel.Health == "degraded"
	allClear := len(suggestions) == 0 && d.guardian.Errors == 0 && !sentinelBad
	if allClear {
		fmt.Println("  Platform looks healthy. No actions needed.")
		return
	}
	if len(suggestions) == 0 {
		fmt.Println("  Issues detected — check guardian and sentinel findings above.")
		return
	}
	fmt.Println("  Suggested actions:")
	for _, s := range suggestions {
		fmt.Printf("    → %s\n", s)
	}
}

func buildSuggestions(d *doctorReport) []string {
	var out []string
	for _, s := range d.services {
		if s.DesiredState == "stopped" {
			continue
		}
		if s.ActualState == "maintenance" {
			out = append(out, fmt.Sprintf("engx services reset %s", s.Name))
		} else if s.ActualState != "running" {
			out = append(out, fmt.Sprintf("check ~/.nexus/logs/%s.log", s.Name))
		}
	}
	connected := false
	for _, a := range d.agents {
		if a.Online {
			connected = true
		}
	}
	if len(d.agents) > 0 && !connected {
		out = append(out, "restart engxa: /tmp/bin/engxa --id local --server http://127.0.0.1:8080 --token local-agent-token --addr 127.0.0.1:9090")
	}
	if len(d.agents) == 0 {
		out = append(out, "start engxa — services won't start without an agent")
	}
	for _, f := range d.guardian.Findings {
		switch f.RuleID {
		case "G-003":
			out = append(out, fmt.Sprintf(
				"high failure rate on %q — check logs: engx logs %s-daemon", f.Target, f.Target))
		case "G-004":
			out = append(out, fmt.Sprintf(
				"service crashes detected — check: engx logs %s-daemon", f.Target))
		case "G-005":
			out = append(out, fmt.Sprintf("add nexus.yaml to project: %s", f.Target))
		}
	}
	return out
}
