// @nexus-project: nexus
// @nexus-path: cmd/engx/main.go
// Phase 12 addition:
//   engx watch — live dashboard that polls the daemon every 2s and
//   redraws the terminal in place. Zero new dependencies — uses ANSI
//   escape codes directly. Press Ctrl-C to exit.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/spf13/cobra"
)

const cliVersion = "0.1.0"

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var socketPath string

	root := &cobra.Command{
		Use:     "engx",
		Short:   "Nexus — Local Developer Control Plane",
		Version: cliVersion,
	}

	root.PersistentFlags().StringVar(
		&socketPath, "socket", daemon.DefaultSocketPath,
		"path to nexus daemon socket",
	)

	root.AddCommand(
		projectCmd(&socketPath),
		registerCmd(&socketPath),
		servicesCmd(&socketPath),
		eventsCmd(&socketPath),
		dropCmd(&socketPath),
		watchCmd(&socketPath),
		versionCmd(),
	)

	return root
}

// ── WATCH COMMAND ─────────────────────────────────────────────────────────────

func watchCmd(socketPath *string) *cobra.Command {
	var interval time.Duration

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Live dashboard — refreshes every 2s (Ctrl-C to exit)",
		RunE: func(cmd *cobra.Command, args []string) error {
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			// Hide cursor for clean redraws.
			fmt.Print("\033[?25l")
			defer fmt.Print("\033[?25h\033[0m\n") // restore on exit

			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			// Draw immediately, then on every tick.
			drawWatch(*socketPath)

			for {
				select {
				case <-sigCh:
					return nil
				case <-ticker.C:
					drawWatch(*socketPath)
				}
			}
		},
	}

	cmd.Flags().DurationVarP(&interval, "interval", "i", 2*time.Second, "refresh interval")
	return cmd
}

// drawWatch clears the screen and redraws the full dashboard.
func drawWatch(socketPath string) {
	// Move cursor to top-left without clearing — avoids flicker.
	fmt.Print("\033[H\033[2J")

	now := time.Now().Format("15:04:05")

	// ── Header ──────────────────────────────────────────────────────────────
	fmt.Printf("\033[1m\033[36m NEXUS WATCH\033[0m  %s  (Ctrl-C to exit)\n", now)
	fmt.Println(strings.Repeat("─", 80))

	// ── Projects + Services ──────────────────────────────────────────────────
	resp, err := sendCommand(socketPath, daemon.CmdProjectStatus,
		daemon.ProjectStatusParams{ProjectID: ""})
	if err != nil {
		fmt.Printf("\n  \033[31m✗ daemon unreachable:\033[0m %v\n", err)
		return
	}

	var statuses []*controllers.ProjectStatus
	if err := json.Unmarshal(resp.Data, &statuses); err != nil {
		fmt.Printf("  decode error: %v\n", err)
		return
	}

	if len(statuses) == 0 {
		fmt.Println("\n  No projects registered.  Run: engx register <path>")
		return
	}

	totalSvcs, healthySvcs := 0, 0

	for _, proj := range statuses {
		fmt.Printf("\n \033[1m%s\033[0m  (%s)\n", proj.ProjectID, proj.ProjectName)
		fmt.Printf("  %-28s %-10s %-12s %-10s\n", "SERVICE", "DESIRED", "ACTUAL", "HEALTH")
		fmt.Println("  " + strings.Repeat("─", 66))

		for _, svc := range proj.Services {
			indicator := "\033[32m●\033[0m"
			if !svc.IsHealthy {
				switch svc.ActualState {
				case state.StateCrashed:
					indicator = "\033[31m✗\033[0m"
				case state.StateMaintenance:
					indicator = "\033[33m⚠\033[0m"
				case state.StateRecovering:
					indicator = "\033[33m↻\033[0m"
				default:
					indicator = "\033[90m○\033[0m"
				}
			}
			fails := ""
			if svc.FailCount > 0 {
				fails = fmt.Sprintf(" \033[31m(%dx)\033[0m", svc.FailCount)
			}
			fmt.Printf("  %-28s %-10s %-12s %s%s\n",
				svc.Name,
				string(svc.DesiredState),
				string(svc.ActualState),
				indicator, fails,
			)
			totalSvcs++
			if svc.IsHealthy {
				healthySvcs++
			}
		}
	}

	// ── Summary bar ──────────────────────────────────────────────────────────
	fmt.Println("\n" + strings.Repeat("─", 80))
	healthColour := "\033[32m"
	if healthySvcs < totalSvcs {
		healthColour = "\033[31m"
	}
	fmt.Printf(" Services: %s%d/%d healthy\033[0m\n", healthColour, healthySvcs, totalSvcs)
}

// ── SOCKET CLIENT ─────────────────────────────────────────────────────────────

func sendCommand(socketPath string, cmd daemon.Command, params any) (*daemon.Response, error) {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("daemon not running — start with: engxd")
	}
	defer conn.Close()

	var rawParams json.RawMessage
	if params != nil {
		rawParams, err = json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("encode params: %w", err)
		}
	}

	req := daemon.Request{
		ID:      fmt.Sprintf("cli-%d", time.Now().UnixNano()),
		Command: cmd,
		Params:  rawParams,
	}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	var resp daemon.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if !resp.OK {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return &resp, nil
}

// ── DROP COMMANDS ─────────────────────────────────────────────────────────────

func dropCmd(socketPath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "drop", Short: "Manage files pending approval"}
	cmd.AddCommand(dropPendingCmd(socketPath), dropApproveCmd(socketPath), dropRejectCmd(socketPath))
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
				return fmt.Errorf("decode: %w", err)
			}
			if len(entries) == 0 {
				fmt.Println("No files pending approval.")
				return nil
			}
			fmt.Printf("\n%-40s %-20s %-8s  %s\n", "FILE", "PROJECT", "CONF", "DESTINATION")
			fmt.Println(strings.Repeat("─", 100))
			for _, e := range entries {
				fmt.Printf("%-40s %-20s %5.0f%%   %s\n",
					e.FileName, e.ProjectID, e.Confidence*100, e.Destination)
			}
			fmt.Printf("\n%d file(s) pending\n\n", len(entries))
			return nil
		},
	}
}

func dropApproveCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "approve <file>", Short: "Approve — move to destination",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath, err := resolvePendingFile(*socketPath, args[0])
			if err != nil {
				return err
			}
			resp, err := sendCommand(*socketPath, daemon.CmdDropApprove,
				daemon.DropApproveParams{FilePath: filePath})
			if err != nil {
				return err
			}
			var result map[string]string
			_ = json.Unmarshal(resp.Data, &result)
			fmt.Printf("\n\033[32m✓\033[0m Approved: %s → %s\n\n", result["file"], result["destination"])
			return nil
		},
	}
}

func dropRejectCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "reject <file>", Short: "Reject — tag UNROUTED__ in place",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath, err := resolvePendingFile(*socketPath, args[0])
			if err != nil {
				return err
			}
			resp, err := sendCommand(*socketPath, daemon.CmdDropReject,
				daemon.DropRejectParams{FilePath: filePath})
			if err != nil {
				return err
			}
			var result map[string]string
			_ = json.Unmarshal(resp.Data, &result)
			fmt.Printf("\n\033[31m✗\033[0m Rejected: %s → %s\n\n", result["file"], result["tagged"])
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
		return "", fmt.Errorf("fetch pending: %w", err)
	}
	var entries []daemon.PendingApproval
	if err := json.Unmarshal(resp.Data, &entries); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	var matches []daemon.PendingApproval
	for _, e := range entries {
		if strings.EqualFold(e.FileName, arg) {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no pending file %q — run: engx drop pending", arg)
	case 1:
		return matches[0].FilePath, nil
	default:
		fmt.Fprintf(os.Stderr, "multiple matches for %q — use full path:\n", arg)
		for _, m := range matches {
			fmt.Fprintf(os.Stderr, "  %s\n", m.FilePath)
		}
		return "", fmt.Errorf("ambiguous filename")
	}
}

// ── REGISTER ──────────────────────────────────────────────────────────────────

func registerCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "register <path>", Short: "Register a project",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectPath, err := filepath.Abs(args[0])
			if err != nil {
				return fmt.Errorf("resolve path: %w", err)
			}
			manifest, err := readNexusManifest(projectPath)
			if err != nil {
				return err
			}
			resp, err := sendCommand(*socketPath, daemon.CmdRegisterProject, daemon.RegisterProjectParams{
				ID: manifest.id, Name: manifest.name, Path: projectPath,
				Language: manifest.language, ProjectType: manifest.projectType,
				ConfigJSON: manifest.rawYAML,
			})
			if err != nil {
				return err
			}
			var result map[string]string
			_ = json.Unmarshal(resp.Data, &result)
			fmt.Printf("✓ Registered: %s (id: %s)\n  Run: engx project status %s\n",
				manifest.name, manifest.id, manifest.id)
			return nil
		},
	}
}

type nexusManifest struct {
	id, name, language, projectType, rawYAML string
}

func readNexusManifest(projectPath string) (*nexusManifest, error) {
	file, err := os.Open(filepath.Join(projectPath, ".nexus.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(".nexus.yaml not found in %s", projectPath)
		}
		return nil, fmt.Errorf("open .nexus.yaml: %w", err)
	}
	defer file.Close()

	m := &nexusManifest{}
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") || t == "" {
			continue
		}
		parts := strings.SplitN(t, ":", 2)
		if len(parts) != 2 {
			continue
		}
		k, v := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		switch k {
		case "name":
			m.name = v
			m.id = strings.ToLower(strings.ReplaceAll(v, " ", "-"))
		case "id":
			m.id = v
		case "language":
			m.language = v
		case "type":
			m.projectType = v
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read .nexus.yaml: %w", err)
	}
	if m.name == "" {
		return nil, fmt.Errorf(".nexus.yaml missing required field: name")
	}
	m.rawYAML = strings.Join(lines, "\n")
	return m, nil
}

// ── PROJECT COMMANDS ──────────────────────────────────────────────────────────

func projectCmd(socketPath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Manage projects"}
	cmd.AddCommand(projectStartCmd(socketPath), projectStopCmd(socketPath), projectStatusCmd(socketPath))
	return cmd
}

func projectStartCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "start <id>", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdProjectStart,
				daemon.ProjectStartParams{ProjectID: args[0]})
			if err != nil {
				return err
			}
			var r map[string]any
			_ = json.Unmarshal(resp.Data, &r)
			if q, _ := r["queued"].(float64); int(q) == 0 {
				fmt.Printf("✓ All services in %q already running\n", args[0])
			} else {
				fmt.Printf("✓ Queued %d service(s)\n  Run: engx project status %s\n", int(q), args[0])
			}
			return nil
		},
	}
}

func projectStopCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "stop <id>", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdProjectStop,
				daemon.ProjectStopParams{ProjectID: args[0]})
			if err != nil {
				return err
			}
			var r map[string]any
			_ = json.Unmarshal(resp.Data, &r)
			if q, _ := r["queued"].(float64); int(q) == 0 {
				fmt.Printf("✓ All services in %q already stopped\n", args[0])
			} else {
				fmt.Printf("✓ Queued %d service(s) to stop\n", int(q))
			}
			return nil
		},
	}
}

func projectStatusCmd(socketPath *string) *cobra.Command {
	var showAll bool
	cmd := &cobra.Command{
		Use: "status [id]", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := ""
			if len(args) > 0 {
				projectID = args[0]
			}
			if !showAll && projectID == "" {
				return fmt.Errorf("provide a project ID or use --all")
			}
			resp, err := sendCommand(*socketPath, daemon.CmdProjectStatus,
				daemon.ProjectStatusParams{ProjectID: projectID})
			if err != nil {
				return err
			}
			if showAll || projectID == "" {
				var ss []*controllers.ProjectStatus
				if err := json.Unmarshal(resp.Data, &ss); err != nil {
					return err
				}
				if len(ss) == 0 {
					fmt.Println("No projects registered.")
					return nil
				}
				for _, s := range ss {
					fmt.Print(renderStatus(s))
				}
				return nil
			}
			var s controllers.ProjectStatus
			if err := json.Unmarshal(resp.Data, &s); err != nil {
				return err
			}
			fmt.Print(renderStatus(&s))
			return nil
		},
	}
	cmd.Flags().BoolVar(&showAll, "all", false, "show all projects")
	return cmd
}

func renderStatus(s *controllers.ProjectStatus) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\nPROJECT: %s (%s)\n", strings.ToUpper(s.ProjectID), s.ProjectName))
	sb.WriteString(fmt.Sprintf("%-30s %-12s %-12s %-10s\n", "SERVICE", "DESIRED", "ACTUAL", "HEALTH"))
	sb.WriteString(strings.Repeat("─", 68) + "\n")
	healthy := 0
	for _, svc := range s.Services {
		ind := "\033[32m✓\033[0m"
		if !svc.IsHealthy {
			ind = "\033[31m✗\033[0m"
		}
		if svc.FailCount > 0 {
			ind += fmt.Sprintf(" (%d)", svc.FailCount)
		}
		sb.WriteString(fmt.Sprintf("%-30s %-12s %-12s %s\n",
			svc.Name, string(svc.DesiredState), string(svc.ActualState), ind))
		if svc.IsHealthy {
			healthy++
		}
	}
	summary := fmt.Sprintf("\n%d/%d healthy", healthy, len(s.Services))
	if healthy == len(s.Services) {
		sb.WriteString("\033[32m" + summary + "\033[0m\n")
	} else {
		sb.WriteString("\033[31m" + summary + "\033[0m\n")
	}
	return sb.String()
}

// ── SERVICES ──────────────────────────────────────────────────────────────────

func servicesCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "services", Short: "List all services",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdServicesList, nil)
			if err != nil {
				return err
			}
			var svcs []*state.Service
			if err := json.Unmarshal(resp.Data, &svcs); err != nil {
				return err
			}
			if len(svcs) == 0 {
				fmt.Println("No services registered.")
				return nil
			}
			fmt.Printf("\n%-30s %-15s %-12s %-12s\n", "SERVICE", "PROJECT", "DESIRED", "ACTUAL")
			fmt.Println(strings.Repeat("─", 72))
			for _, s := range svcs {
				fmt.Printf("%-30s %-15s %-12s %-12s\n",
					s.Name, s.Project, string(s.DesiredState), string(s.ActualState))
			}
			fmt.Printf("\nTotal: %d\n\n", len(svcs))
			return nil
		},
	}
}

// ── EVENTS ────────────────────────────────────────────────────────────────────

func eventsCmd(socketPath *string) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use: "events", Short: "Show recent events",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdEventsList, daemon.EventsListParams{Limit: limit})
			if err != nil {
				return err
			}
			var events []*state.Event
			if err := json.Unmarshal(resp.Data, &events); err != nil {
				return err
			}
			if len(events) == 0 {
				fmt.Println("No events yet.")
				return nil
			}
			fmt.Printf("\n%-20s %-25s %-20s\n", "TIME", "TYPE", "SERVICE")
			fmt.Println(strings.Repeat("─", 68))
			for _, e := range events {
				fmt.Printf("%-20s %-25s %-20s\n",
					e.CreatedAt.Format("01-02 15:04:05"), string(e.Type), e.ServiceID)
			}
			fmt.Printf("\n%d events\n\n", len(events))
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "number of events")
	return cmd
}

// ── VERSION ───────────────────────────────────────────────────────────────────

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use: "version", Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("engx version %s\n", cliVersion)
		},
	}
}
