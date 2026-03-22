// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_insights.go
// Developer insight commands — why, activity, and ps --detail.
// All read-only: no writes to any service.
// Data sources: Nexus /services, /events, Guardian /guardian/findings,
//               Forge /history (via shared helpers).
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// ── WHY ───────────────────────────────────────────────────────────────────────

// whyCmd explains why a project is not running or is unhealthy.
// Aggregates service state, last build result, and Guardian findings
// into a single actionable answer.
func whyCmd(httpAddr *string) *cobra.Command {
	return &cobra.Command{
		Use:   "why <project>",
		Short: "Explain why a project is unhealthy or not running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWhy(*httpAddr, args[0])
		},
	}
}

func runWhy(httpAddr, id string) error {
	fmt.Println()
	fmt.Printf("  %s — why\n", id)
	fmt.Println("  " + strings.Repeat("─", 48))

	problems := 0

	// ── 1. Service state ──────────────────────────────────────────────────
	svcs := getProjectServices(httpAddr, id)
	for _, s := range svcs {
		if s.desiredState == "stopped" {
			continue
		}
		switch s.actualState {
		case "running":
			fmt.Printf("  ✓ service       %s is running\n", s.id)
		case "maintenance":
			fmt.Printf("  ✗ service       %s is in maintenance (fail count: %d)\n", s.id, s.failCount)
			fmt.Printf("    → reset:      engx services reset %s\n", s.id)
			problems++
		default:
			msg := "not running"
			if s.failCount > 0 {
				msg = fmt.Sprintf("not running — failed %d time(s)", s.failCount)
			}
			fmt.Printf("  ✗ service       %s %s\n", s.id, msg)
			fmt.Printf("    → check logs: engx logs %s\n", s.id)
			problems++
		}
	}
	if len(svcs) == 0 {
		fmt.Printf("  ○ service       no services registered for %q\n", id)
		fmt.Printf("    → register:   engx register %s\n", id)
		problems++
	}

	// ── 2. Last build result ──────────────────────────────────────────────
	execs := fetchProjectHistory(httpAddr, id, 5)
	if len(execs) > 0 {
		last := execs[0]
		ago := time.Since(last.StartedAt).Round(time.Second)
		switch last.Status {
		case "success":
			fmt.Printf("  ✓ last build    success (%s ago)\n", ago)
		case "failure":
			fmt.Printf("  ✗ last build    failed (%s ago)\n", ago)
			fmt.Printf("    → rebuild:    engx build %s\n", id)
			problems++
		case "denied":
			fmt.Printf("  ✗ last build    denied — preflight blocked (%s ago)\n", ago)
			fmt.Printf("    → check:      engx check %s\n", id)
			problems++
		}
	} else {
		fmt.Printf("  ○ last build    never built\n")
		fmt.Printf("    → build:      engx build %s\n", id)
		problems++
	}

	// ── 3. Guardian findings ──────────────────────────────────────────────
	findings := fetchGuardianFindingsForProject(id)
	errors := 0
	for _, f := range findings {
		if f.Severity == "error" {
			fmt.Printf("  ✗ %-12s %s\n", f.RuleID, truncate(f.Message, 60))
			errors++
			problems++
		}
	}
	for _, f := range findings {
		if f.Severity == "warning" {
			fmt.Printf("  ○ %-12s %s\n", f.RuleID, truncate(f.Message, 60))
		}
	}
	if len(findings) == 0 && errors == 0 {
		fmt.Println("  ✓ guardian      no findings")
	}

	fmt.Println()
	if problems == 0 {
		fmt.Printf("  %s looks healthy — no problems found.\n", id)
	} else {
		fmt.Printf("  %d problem(s) found. Start with the first ✗ above.\n", problems)
	}
	fmt.Println()
	return nil
}

// ── ACTIVITY ──────────────────────────────────────────────────────────────────

// activityCmd shows a merged chronological feed of platform events
// and Forge executions for the last N minutes.
func activityCmd(httpAddr *string) *cobra.Command {
	var minutes int
	cmd := &cobra.Command{
		Use:   "activity",
		Short: "Show platform activity — events and executions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runActivity(*httpAddr, minutes)
		},
	}
	cmd.Flags().IntVarP(&minutes, "minutes", "m", 120, "how far back to look (minutes)")
	return cmd
}

type activityEntry struct {
	At      time.Time
	Kind    string // "event" | "exec"
	Service string
	Label   string
	Status  string
}

func runActivity(httpAddr string, minutes int) error {
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute)

	// ── Nexus events ──────────────────────────────────────────────────────
	var eventsResult struct {
		Data []struct {
			Type      string    `json:"type"`
			ServiceID string    `json:"service_id"`
			CreatedAt time.Time `json:"created_at"`
			Outcome   string    `json:"outcome"`
		} `json:"data"`
	}
	var entries []activityEntry
	if err := getJSON(fmt.Sprintf("%s/events?limit=200", httpAddr), &eventsResult); err == nil {
		for _, e := range eventsResult.Data {
			if e.CreatedAt.Before(cutoff) {
				continue
			}
			label := eventLabel(e.Type)
			if label == "" {
				continue
			}
			entries = append(entries, activityEntry{
				At:      e.CreatedAt,
				Kind:    "event",
				Service: e.ServiceID,
				Label:   label,
				Status:  e.Outcome,
			})
		}
	}

	// ── Forge execution history ───────────────────────────────────────────
	var histResult struct {
		Data []struct {
			Target    string    `json:"target"`
			Intent    string    `json:"intent"`
			Status    string    `json:"status"`
			StartedAt time.Time `json:"started_at"`
		} `json:"data"`
	}
	if err := getJSON("http://127.0.0.1:8082/history?limit=100", &histResult); err == nil {
		for _, h := range histResult.Data {
			if h.StartedAt.Before(cutoff) {
				continue
			}
			entries = append(entries, activityEntry{
				At:      h.StartedAt,
				Kind:    "exec",
				Service: h.Target,
				Label:   h.Intent,
				Status:  h.Status,
			})
		}
	}

	if len(entries) == 0 {
		fmt.Printf("\n  No activity in the last %d minutes.\n\n", minutes)
		return nil
	}

	// Sort descending by time (simple insertion sort — small N)
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].At.After(entries[j-1].At); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}

	fmt.Println()
	fmt.Printf("  Platform activity — last %d minutes\n", minutes)
	fmt.Println("  " + strings.Repeat("─", 56))
	fmt.Printf("  %-5s  %-9s  %-24s  %-10s  %s\n", "TIME", "KIND", "SERVICE", "ACTION", "STATUS")
	fmt.Println("  " + strings.Repeat("─", 56))

	for _, e := range entries {
		icon := "○"
		switch e.Status {
		case "success", "":
			icon = "✓"
		case "failure", "error":
			icon = "✗"
		}
		fmt.Printf("  %s  %s %-7s  %-24s  %-10s  %s\n",
			e.At.Format("15:04"),
			icon,
			e.Kind,
			truncate(e.Service, 24),
			truncate(e.Label, 10),
			e.Status,
		)
	}
	fmt.Println()
	return nil
}

func eventLabel(t string) string {
	switch t {
	case "SERVICE_STARTED":
		return "started"
	case "SERVICE_STOPPED":
		return "stopped"
	case "SERVICE_CRASHED":
		return "crashed"
	case "SERVICE_HEALED":
		return "healed"
	case "STATE_CHANGED":
		return "state-change"
	case "SYSTEM_ALERT":
		return "alert"
	default:
		return ""
	}
}

// ── PS --DETAIL ───────────────────────────────────────────────────────────────

// psDetailCmd shows a rich service table with uptime, build history, and fail count.
func psDetailCmd(httpAddr *string) *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "Show all services with health detail",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPsDetail(*httpAddr)
		},
	}
}

func runPsDetail(httpAddr string) error {
	var svcResult struct {
		Data []struct {
			ID           string    `json:"id"`
			Project      string    `json:"project"`
			DesiredState string    `json:"desired_state"`
			ActualState  string    `json:"actual_state"`
			FailCount    int       `json:"fail_count"`
			UpdatedAt    time.Time `json:"updated_at"`
		} `json:"data"`
	}
	if err := getJSON(httpAddr+"/services", &svcResult); err != nil {
		return fmt.Errorf("cannot reach nexus: %w", err)
	}

	// Fetch execution history for last-build info per project.
	lastBuild := map[string]struct {
		status string
		ago    string
	}{}
	var histResult struct {
		Data []struct {
			Target    string    `json:"target"`
			Intent    string    `json:"intent"`
			Status    string    `json:"status"`
			StartedAt time.Time `json:"started_at"`
		} `json:"data"`
	}
	if err := getJSON("http://127.0.0.1:8082/history?limit=200", &histResult); err == nil {
		seen := map[string]bool{}
		for _, h := range histResult.Data {
			if h.Intent == "build" && !seen[h.Target] {
				seen[h.Target] = true
				ago := time.Since(h.StartedAt).Round(time.Minute)
				lastBuild[h.Target] = struct {
					status string
					ago    string
				}{h.Status, formatUptime(ago.Seconds())}
			}
		}
	}

	fmt.Println()
	fmt.Printf("  %-26s %-12s %-8s %-14s %s\n",
		"SERVICE", "STATE", "FAILS", "LAST BUILD", "PROJECT")
	fmt.Println("  " + strings.Repeat("─", 72))

	for _, s := range svcResult.Data {
		if s.DesiredState == "stopped" {
			continue
		}
		icon := "✓"
		if s.ActualState != "running" {
			icon = "✗"
		}
		failStr := "—"
		if s.FailCount > 0 {
			failStr = fmt.Sprintf("%d ⚠", s.FailCount)
		}
		buildStr := "—"
		if b, ok := lastBuild[s.Project]; ok {
			buildStr = fmt.Sprintf("%s %s ago", b.status, b.ago)
		}
		fmt.Printf("  %s %-24s %-12s %-8s %-14s %s\n",
			icon,
			truncate(s.ID, 24),
			s.ActualState,
			failStr,
			truncate(buildStr, 14),
			s.Project,
		)
	}
	fmt.Println()
	return nil
}

// ── SHARED HELPERS ────────────────────────────────────────────────────────────

type execRecord struct {
	Target    string    `json:"target"`
	Intent    string    `json:"intent"`
	Status    string    `json:"status"`
	Error     string    `json:"error"`
	StartedAt time.Time `json:"started_at"`
}

type guardianFinding struct {
	RuleID   string `json:"rule_id"`
	Severity string `json:"severity"`
	Target   string `json:"target"`
	Message  string `json:"message"`
}

// fetchProjectHistory returns the last N Forge executions for a project.
func fetchProjectHistory(httpAddr, projectID string, limit int) []execRecord {
	var result struct {
		Data []execRecord `json:"data"`
	}
	url := fmt.Sprintf("http://127.0.0.1:8082/history?limit=%d", limit)
	if err := getJSON(url, &result); err != nil {
		return nil
	}
	var out []execRecord
	for _, r := range result.Data {
		if r.Target == projectID {
			out = append(out, r)
		}
	}
	return out
}

// fetchGuardianFindingsForProject returns Guardian findings for one project.
func fetchGuardianFindingsForProject(projectID string) []guardianFinding {
	var result struct {
		Data struct {
			Findings []guardianFinding `json:"findings"`
		} `json:"data"`
	}
	if err := getJSON("http://127.0.0.1:8085/guardian/findings", &result); err != nil {
		return nil
	}
	var out []guardianFinding
	for _, f := range result.Data.Findings {
		if f.Target == projectID {
			out = append(out, f)
		}
	}
	return out
}
