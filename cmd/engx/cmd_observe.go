// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_observe.go
// Watch and drop commands — live dashboard and file approval workflow.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/spf13/cobra"
)

// ── WATCH ─────────────────────────────────────────────────────────────────────

func watchCmd(socketPath *string) *cobra.Command {
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Live dashboard (Ctrl-C to exit)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			fmt.Print("\033[?25l")
			defer fmt.Print("\033[?25h\033[0m\n")
			drawWatch(*socketPath)
			for range ticker.C {
				drawWatch(*socketPath)
			}
			return nil
		},
	}
	cmd.Flags().DurationVarP(&interval, "interval", "i", 2*time.Second, "refresh interval")
	return cmd
}

func drawWatch(socketPath string) {
	fmt.Print("\033[H\033[2J")
	fmt.Printf("\033[1m\033[36m NEXUS WATCH\033[0m  %s\n", time.Now().Format("15:04:05"))
	fmt.Println(strings.Repeat("─", 78))

	resp, err := sendCommand(socketPath, daemon.CmdProjectStatus,
		daemon.ProjectStatusParams{ProjectID: ""})
	if err != nil {
		fmt.Printf("\n  \033[31m✗\033[0m daemon unreachable: %v\n", err)
		return
	}
	var statuses []*controllers.ProjectStatus
	if err := json.Unmarshal(resp.Data, &statuses); err != nil {
		return
	}
	if len(statuses) == 0 {
		fmt.Println("\n  No projects registered.")
		return
	}
	total, healthy := 0, 0
	for _, proj := range statuses {
		fmt.Printf("\n \033[1m%s\033[0m (%s)\n", proj.ProjectID, proj.ProjectName)
		fmt.Printf("  %-28s %-10s %-12s\n", "SERVICE", "DESIRED", "ACTUAL")
		fmt.Println("  " + strings.Repeat("─", 54))
		for _, svc := range proj.Services {
			ind := "\033[32m●\033[0m"
			if !svc.IsHealthy {
				switch svc.ActualState {
				case state.StateCrashed:
					ind = "\033[31m✗\033[0m"
				case state.StateMaintenance:
					ind = "\033[33m⚠\033[0m"
				default:
					ind = "\033[90m○\033[0m"
				}
			}
			fmt.Printf("  %-28s %-10s %-12s %s\n",
				svc.Name, string(svc.DesiredState), string(svc.ActualState), ind)
			total++
			if svc.IsHealthy {
				healthy++
			}
		}
	}
	fmt.Println("\n" + strings.Repeat("─", 78))
	col := "\033[32m"
	if healthy < total {
		col = "\033[31m"
	}
	fmt.Printf(" %s%d/%d healthy\033[0m\n", col, healthy, total)
}

// ── DROP ──────────────────────────────────────────────────────────────────────

func dropCmd(socketPath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "drop", Short: "Manage files pending approval"}
	cmd.AddCommand(
		dropPendingCmd(socketPath),
		dropApproveCmd(socketPath),
		dropRejectCmd(socketPath),
		dropTrainCmd(socketPath),
	)
	return cmd
}

func dropPendingCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "pending", Short: "List files awaiting approval",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdDropPending, nil)
			if err != nil {
				return err
			}
			var entries []daemon.PendingApproval
			if err := json.Unmarshal(resp.Data, &entries); err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Println("No files pending.")
				return nil
			}
			fmt.Printf("\n%-38s %-18s %-7s  %s\n", "FILE", "PROJECT", "CONF", "DESTINATION")
			fmt.Println(strings.Repeat("─", 96))
			for _, e := range entries {
				fmt.Printf("%-38s %-18s %5.0f%%   %s\n",
					e.FileName, e.ProjectID, e.Confidence*100, e.Destination)
			}
			fmt.Printf("\n%d pending\n\n", len(entries))
			return nil
		},
	}
}

func dropApproveCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "approve <file>", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fp, err := resolvePendingFile(*socketPath, args[0])
			if err != nil {
				return err
			}
			resp, err := sendCommand(*socketPath, daemon.CmdDropApprove,
				daemon.DropApproveParams{FilePath: fp})
			if err != nil {
				return err
			}
			var r map[string]string
			_ = json.Unmarshal(resp.Data, &r)
			fmt.Printf("\033[32m✓\033[0m %s → %s\n", r["file"], r["destination"])
			return nil
		},
	}
}

func dropRejectCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "reject <file>", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fp, err := resolvePendingFile(*socketPath, args[0])
			if err != nil {
				return err
			}
			resp, err := sendCommand(*socketPath, daemon.CmdDropReject,
				daemon.DropRejectParams{FilePath: fp})
			if err != nil {
				return err
			}
			var r map[string]string
			_ = json.Unmarshal(resp.Data, &r)
			fmt.Printf("\033[31m✗\033[0m %s → %s\n", r["file"], r["tagged"])
			return nil
		},
	}
}

func dropTrainCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "train", Short: "Train the ML classifier from download history",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdDropTrain, nil)
			if err != nil {
				return err
			}
			var result map[string]any
			if err := json.Unmarshal(resp.Data, &result); err != nil {
				return err
			}
			fmt.Printf("\n\033[32m✓\033[0m Classifier trained\n")
			if v, ok := result["examples_used"]; ok {
				fmt.Printf("  Examples used: %.0f\n", v.(float64))
			}
			if v, ok := result["vocab_size"]; ok {
				fmt.Printf("  Vocabulary:    %.0f tokens\n", v.(float64))
			}
			if v, ok := result["trained_at"]; ok {
				fmt.Printf("  Trained at:    %s\n", v)
			}
			fmt.Println("\n  Layer 5 (ML) is now active for all future drops.")
			return nil
		},
	}
}

func resolvePendingFile(socketPath, arg string) (string, error) {
	if filepath.IsAbs(arg) {
		return arg, nil
	}
	resp, err := sendCommand(socketPath, daemon.CmdDropPending, nil)
	if err != nil {
		return "", err
	}
	var entries []daemon.PendingApproval
	if err := json.Unmarshal(resp.Data, &entries); err != nil {
		return "", err
	}
	var matches []daemon.PendingApproval
	for _, e := range entries {
		if strings.EqualFold(e.FileName, arg) {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no pending file %q", arg)
	case 1:
		return matches[0].FilePath, nil
	default:
		for _, m := range matches {
			fmt.Fprintf(os.Stderr, "  %s\n", m.FilePath)
		}
		return "", fmt.Errorf("ambiguous — use full path")
	}
}
