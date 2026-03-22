// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_history.go
// engx history <project> — execution timeline for a project.
// Shows last N Forge executions with intent, status, duration, actor, and denial reason.
// Read-only: GET /history on Forge.
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func historyCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "history <project>",
		Short: "Show execution history for a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHistory(args[0], limit)
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 15, "number of executions to show")
	return cmd
}

func runHistory(projectID string, limit int) error {
	var result struct {
		Data []struct {
			Target     string    `json:"target"`
			Intent     string    `json:"intent"`
			Status     string    `json:"status"`
			Error      string    `json:"error"`
			ActorSub   string    `json:"actor_sub"`
			StartedAt  time.Time `json:"started_at"`
			FinishedAt time.Time `json:"finished_at"`
			DurationMS int64     `json:"duration_ms"`
		} `json:"data"`
	}
	url := fmt.Sprintf("http://127.0.0.1:8082/history?limit=%d", limit*4) // over-fetch, filter by target
	if err := getJSON(url, &result); err != nil {
		return fmt.Errorf("forge unavailable: %w", err)
	}

	// Filter to this project
	type row struct {
		At       time.Time
		Intent   string
		Status   string
		Duration string
		Actor    string
		Note     string
	}
	var rows []row
	for _, r := range result.Data {
		if r.Target != projectID {
			continue
		}
		dur := "—"
		if r.DurationMS > 0 {
			dur = fmt.Sprintf("%dms", r.DurationMS)
			if r.DurationMS >= 1000 {
				dur = fmt.Sprintf("%.1fs", float64(r.DurationMS)/1000)
			}
		}
		actor := r.ActorSub
		if actor == "" {
			actor = "anonymous"
		}
		note := ""
		if r.Status == "denied" && r.Error != "" {
			note = "← " + truncate(r.Error, 40)
		}
		rows = append(rows, row{
			At:       r.StartedAt,
			Intent:   r.Intent,
			Status:   r.Status,
			Duration: dur,
			Actor:    actor,
			Note:     note,
		})
		if len(rows) >= limit {
			break
		}
	}

	if len(rows) == 0 {
		fmt.Printf("\n  No execution history for %q.\n\n", projectID)
		fmt.Printf("  → first build:  engx build %s\n\n", projectID)
		return nil
	}

	fmt.Println()
	fmt.Printf("  %s — execution history (last %d)\n", projectID, len(rows))
	fmt.Println("  " + strings.Repeat("─", 72))
	fmt.Printf("  %-5s  %-10s  %-9s  %-7s  %-20s  %s\n",
		"TIME", "INTENT", "STATUS", "DURATION", "ACTOR", "NOTE")
	fmt.Println("  " + strings.Repeat("─", 72))

	for _, r := range rows {
		icon := statusIcon(r.Status)
		fmt.Printf("  %s  %s %-8s  %-9s  %-7s  %-20s  %s\n",
			r.At.Format("15:04"),
			icon,
			truncate(r.Intent, 8),
			r.Status,
			r.Duration,
			truncate(r.Actor, 20),
			r.Note,
		)
	}
	fmt.Println()
	return nil
}

func statusIcon(status string) string {
	switch status {
	case "success":
		return "✓"
	case "failure":
		return "✗"
	case "denied":
		return "○"
	default:
		return " "
	}
}
