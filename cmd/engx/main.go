// @nexus-project: nexus
// @nexus-path: cmd/engx/main.go
// Phase 14 addition:
//   engx agents — lists all registered remote agents with online/offline status.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	var (
		socketPath string
		httpAddr   string
	)

	root := &cobra.Command{
		Use:     "engx",
		Short:   "Nexus — Local Developer Control Plane",
		Version: cliVersion,
	}

	root.PersistentFlags().StringVar(&socketPath, "socket", daemon.DefaultSocketPath,
		"path to nexus daemon socket")
	root.PersistentFlags().StringVar(&httpAddr, "http", "http://127.0.0.1:8080",
		"engxd HTTP API address (for agent commands)")

	root.AddCommand(
		projectCmd(&socketPath),
		registerCmd(&socketPath),
		servicesCmd(&socketPath),
		eventsCmd(&socketPath),
		dropCmd(&socketPath),
		watchCmd(&socketPath),
		agentsCmd(&httpAddr),
		versionCmd(),
	)

	return root
}

// ── AGENTS COMMAND ────────────────────────────────────────────────────────────

func agentsCmd(httpAddr *string) *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List registered remote agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := http.Get(*httpAddr + "/agents")
			if err != nil {
				return fmt.Errorf("cannot reach engxd at %s: %w", *httpAddr, err)
			}
			defer resp.Body.Close()

			var result struct {
				OK   bool `json:"ok"`
				Data []struct {
					ID           string `json:"id"`
					Hostname     string `json:"hostname"`
					Address      string `json:"address"`
					Online       bool   `json:"online"`
					LastSeen     string `json:"last_seen"`
					RegisteredAt string `json:"registered_at"`
				} `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}

			if len(result.Data) == 0 {
				fmt.Println("\nNo agents registered.")
				fmt.Println("  Start an agent on a remote machine:")
				fmt.Printf("  engxa --server %s --token <secret>\n\n", *httpAddr)
				return nil
			}

			fmt.Printf("\n%-20s %-20s %-18s %-8s %s\n",
				"AGENT ID", "HOSTNAME", "ADDRESS", "STATUS", "LAST SEEN")
			fmt.Println(strings.Repeat("─", 86))

			for _, a := range result.Data {
				status := "\033[32m● online\033[0m "
				if !a.Online {
					status = "\033[31m○ offline\033[0m"
				}
				lastSeen := a.LastSeen
				if lastSeen == "" {
					lastSeen = "never"
				}
				fmt.Printf("%-20s %-20s %-18s %-18s %s\n",
					a.ID, a.Hostname, a.Address, status, lastSeen)
			}

			online := 0
			for _, a := range result.Data {
				if a.Online {
					online++
				}
			}
			fmt.Printf("\n%d agent(s) registered, %d online\n\n",
				len(result.Data), online)
			return nil
		},
	}
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
		return nil, fmt.Errorf("send: %w", err)
	}

	var resp daemon.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return &resp, nil
}

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
	now := time.Now().Format("15:04:05")
	fmt.Printf("\033[1m\033[36m NEXUS WATCH\033[0m  %s\n", now)
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

// ── DROP COMMANDS ─────────────────────────────────────────────────────────────

func dropCmd(socketPath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "drop", Short: "Manage files pending approval"}
	cmd.AddCommand(dropPendingCmd(socketPath), dropApproveCmd(socketPath), dropRejectCmd(socketPath), dropTrainCmd(socketPath))
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
			resp, err := sendCommand(*socketPath, daemon.CmdDropApprove, daemon.DropApproveParams{FilePath: fp})
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
			resp, err := sendCommand(*socketPath, daemon.CmdDropReject, daemon.DropRejectParams{FilePath: fp})
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
			fmt.Printf("\n  Layer 5 (ML) is now active for all future drops.\n\n")
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

// ── REGISTER ──────────────────────────────────────────────────────────────────

func registerCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "register <path>", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectPath, _ := filepath.Abs(args[0])
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
			var r map[string]string
			_ = json.Unmarshal(resp.Data, &r)
			fmt.Printf("✓ Registered: %s (id: %s)\n", manifest.name, manifest.id)
			return nil
		},
	}
}

type nexusManifest struct{ id, name, language, projectType, rawYAML string }

func readNexusManifest(projectPath string) (*nexusManifest, error) {
	file, err := os.Open(filepath.Join(projectPath, ".nexus.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(".nexus.yaml not found in %s", projectPath)
		}
		return nil, err
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
	if m.name == "" {
		return nil, fmt.Errorf(".nexus.yaml missing: name")
	}
	m.rawYAML = strings.Join(lines, "\n")
	return m, nil
}

// ── PROJECT / SERVICES / EVENTS / VERSION ─────────────────────────────────────

func projectCmd(socketPath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Manage projects"}
	cmd.AddCommand(projectStartCmd(socketPath), projectStopCmd(socketPath), projectStatusCmd(socketPath))
	return cmd
}

func projectStartCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{Use: "start <id>", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdProjectStart, daemon.ProjectStartParams{ProjectID: args[0]})
			if err != nil {
				return err
			}
			var r map[string]any
			_ = json.Unmarshal(resp.Data, &r)
			if q, _ := r["queued"].(float64); int(q) == 0 {
				fmt.Printf("✓ Already running\n")
			} else {
				fmt.Printf("✓ Queued %d service(s)\n", int(q))
			}
			return nil
		},
	}
}

func projectStopCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{Use: "stop <id>", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdProjectStop, daemon.ProjectStopParams{ProjectID: args[0]})
			if err != nil {
				return err
			}
			var r map[string]any
			_ = json.Unmarshal(resp.Data, &r)
			if q, _ := r["queued"].(float64); int(q) == 0 {
				fmt.Printf("✓ Already stopped\n")
			} else {
				fmt.Printf("✓ Queued %d service(s) to stop\n", int(q))
			}
			return nil
		},
	}
}

func projectStatusCmd(socketPath *string) *cobra.Command {
	var all bool
	cmd := &cobra.Command{Use: "status [id]", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			if !all && id == "" {
				return fmt.Errorf("provide id or --all")
			}
			resp, err := sendCommand(*socketPath, daemon.CmdProjectStatus, daemon.ProjectStatusParams{ProjectID: id})
			if err != nil {
				return err
			}
			if all || id == "" {
				var ss []*controllers.ProjectStatus
				if err := json.Unmarshal(resp.Data, &ss); err != nil {
					return err
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
	cmd.Flags().BoolVar(&all, "all", false, "show all")
	return cmd
}

func renderStatus(s *controllers.ProjectStatus) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\nPROJECT: %s (%s)\n", strings.ToUpper(s.ProjectID), s.ProjectName))
	sb.WriteString(fmt.Sprintf("%-30s %-12s %-12s\n", "SERVICE", "DESIRED", "ACTUAL"))
	sb.WriteString(strings.Repeat("─", 56) + "\n")
	h := 0
	for _, svc := range s.Services {
		ind := "\033[32m✓\033[0m"
		if !svc.IsHealthy {
			ind = "\033[31m✗\033[0m"
		}
		sb.WriteString(fmt.Sprintf("%-30s %-12s %-12s %s\n",
			svc.Name, string(svc.DesiredState), string(svc.ActualState), ind))
		if svc.IsHealthy {
			h++
		}
	}
	n := len(s.Services)
	if h == n {
		sb.WriteString(fmt.Sprintf("\033[32m%d/%d healthy\033[0m\n", h, n))
	} else {
		sb.WriteString(fmt.Sprintf("\033[31m%d/%d healthy\033[0m\n", h, n))
	}
	return sb.String()
}

func servicesCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{Use: "services", Short: "List services",
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
				fmt.Println("No services.")
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

func eventsCmd(socketPath *string) *cobra.Command {
	var limit int
	cmd := &cobra.Command{Use: "events", Short: "Show recent events",
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
				fmt.Println("No events.")
				return nil
			}
			fmt.Printf("\n%-20s %-25s %-20s\n", "TIME", "TYPE", "SERVICE")
			fmt.Println(strings.Repeat("─", 68))
			for _, e := range events {
				fmt.Printf("%-20s %-25s %s\n",
					e.CreatedAt.Format("01-02 15:04:05"), string(e.Type), e.ServiceID)
			}
			fmt.Printf("\n%d events\n\n", len(events))
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "count")
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("engx version %s\n", cliVersion)
		},
	}
}
