// @nexus-project: nexus
// @nexus-path: cmd/engx/main.go
// Phase 14 addition:
//   engx agents — lists all registered remote agents with online/offline status.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/Harshmaury/Nexus/internal/state"
	canon "github.com/Harshmaury/Canon/identity"
	"github.com/spf13/cobra"
)

const cliVersion = "0.5.0"

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
		registerCmd(&socketPath, &httpAddr),
		servicesCmd(&socketPath, &httpAddr),
		eventsCmd(&socketPath),
		dropCmd(&socketPath),
		watchCmd(&socketPath),
		agentsCmd(&httpAddr),
		platformCmd(&socketPath, &httpAddr),
		doctorCmd(&httpAddr),
		logsFollowCmd(),
		buildCmd(&httpAddr),
		checkCmd(&httpAddr),
		runCmd(&socketPath, &httpAddr),
		initCmd(&socketPath, &httpAddr),
		traceCmd(),
		versionCmd(),
		statusCmd(&httpAddr),
		sentinelCmd(&httpAddr),
		workflowCmd(&httpAddr),
		triggerCmd(&httpAddr),
		guardCmd(&httpAddr),
		onCmd(&httpAddr),
		execCmd(&httpAddr),
		ciCmd(&httpAddr),
		eventsStreamCmd(&httpAddr),
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

func registerCmd(socketPath, httpAddr *string) *cobra.Command {
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

			// ADR-022: auto-register default service if runtime section present
			if manifest.runtimeProvider != "" && manifest.runtimeCommand != "" {
				if err := registerDefaultService(*httpAddr, manifest, projectPath); err != nil {
					fmt.Printf("  WARNING: service registration skipped: %v\n", err)
				}
			}
			return nil
		},
	}
}

// registerDefaultService calls POST /services/register for the project's
// default service derived from the runtime section of .nexus.yaml (ADR-022).
func registerDefaultService(httpAddr string, m *nexusManifest, projectPath string) error {
	serviceID := m.id + "-daemon"
	cfg, err := json.Marshal(map[string]any{
		"command": m.runtimeCommand,
		"args":    m.runtimeArgs,
		"dir":     m.runtimeDir,
	})
	if err != nil {
		return fmt.Errorf("build service config: %w", err)
	}
	body, _ := json.Marshal(map[string]string{
		"id":       serviceID,
		"name":     serviceID,
		"project":  m.id,
		"provider": m.runtimeProvider,
		"config":   string(cfg),
	})
	resp, err := http.Post(httpAddr+"/services/register",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("POST /services/register: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST /services/register: HTTP %d", resp.StatusCode)
	}
	fmt.Printf("  ✓ Service registered: %s (provider=%s)\n", serviceID, m.runtimeProvider)
	return nil
}

type nexusManifest struct {
	id, name, language, projectType, rawYAML string
	// runtime fields — parsed from runtime: section of .nexus.yaml (ADR-022)
	runtimeProvider string // "process" | "docker" | "k8s"
	runtimeCommand  string // binary name or path
	runtimeArgs     []string
	runtimeDir      string
}

func readNexusManifest(projectPath string) (*nexusManifest, error) {
	file, err := os.Open(filepath.Join(projectPath, ".nexus.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(".nexus.yaml not found in %s", projectPath)
		}
		return nil, err
	}
	defer file.Close()

	m, lines := scanManifestLines(file)
	if m.name == "" {
		return nil, fmt.Errorf(".nexus.yaml missing: name")
	}
	if m.runtimeDir == "" {
		m.runtimeDir = projectPath
	}
	m.rawYAML = strings.Join(lines, "\n")
	return m, nil
}

// scanManifestLines reads .nexus.yaml line by line and populates a nexusManifest.
func scanManifestLines(r io.Reader) (*nexusManifest, []string) {
	m := &nexusManifest{}
	var lines []string
	inRuntime := false
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") || t == "" {
			continue
		}
		if t == "runtime:" {
			inRuntime = true
			continue
		}
		if inRuntime && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inRuntime = false
		}
		parts := strings.SplitN(t, ":", 2)
		if len(parts) != 2 {
			continue
		}
		k, v := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if inRuntime {
			parseRuntimeField(m, k, v)
		} else {
			parseTopLevelField(m, k, v)
		}
	}
	return m, lines
}

// parseRuntimeField populates runtime fields from the runtime: section.
func parseRuntimeField(m *nexusManifest, k, v string) {
	switch k {
	case "provider":
		m.runtimeProvider = v
	case "command":
		m.runtimeCommand = v
	case "dir":
		m.runtimeDir = v
	}
}

// parseTopLevelField populates top-level fields from .nexus.yaml.
func parseTopLevelField(m *nexusManifest, k, v string) {
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

func servicesCmd(socketPath, httpAddr *string) *cobra.Command {
	cmd := &cobra.Command{Use: "services", Short: "List services",
		RunE: func(c *cobra.Command, args []string) error {
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
	cmd.AddCommand(serviceResetCmd(httpAddr))
	return cmd
}

// serviceResetCmd handles engx services reset <id> (ADR-023).
func serviceResetCmd(httpAddr *string) *cobra.Command {
	return &cobra.Command{
		Use:   "reset <service-id>",
		Short: "Reset a service from maintenance or crash loop back to stopped",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			resp, err := http.Post(
				*httpAddr+"/services/"+id+"/reset",
				"application/json", nil)
			if err != nil {
				return fmt.Errorf("cannot reach engxd: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				return fmt.Errorf("service %q not found", id)
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("reset failed: HTTP %d", resp.StatusCode)
			}
			fmt.Printf("✓ %s reset to stopped\n", id)
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

// ── PLATFORM COMMAND ─────────────────────────────────────────────────────────

// platformCmd provides engx platform start|stop|status — one command
// to control the entire platform instead of running project commands
// individually for each of the 8 services.
func platformCmd(socketPath, httpAddr *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "platform",
		Short: "Control the full platform (start/stop/status all services)",
	}
	cmd.AddCommand(
		platformStartCmd(socketPath, httpAddr),
		platformStopCmd(socketPath),
		platformStatusCmd(socketPath, httpAddr),
	)
	return cmd
}

// platformProjects is the ordered list of platform projects.
// Nexus is excluded — it must be running before engx can talk to the daemon.
var platformProjects = []string{
	"atlas", "forge",
	"metrics", "navigator", "guardian", "observer", "sentinel",
}

// platformServiceIDs maps project IDs to their default service ID.
// Used by platform start to reset services before queuing (ADR-023).
var platformServiceIDs = []string{
	"atlas-daemon", "forge-daemon",
	"metrics-daemon", "navigator-daemon", "guardian-daemon",
	"observer-daemon", "sentinel-daemon",
}

func platformStartCmd(socketPath, httpAddr *string) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start all platform services",
		RunE: func(cmd *cobra.Command, args []string) error {
			// ADR-023: reset fail counts before queuing — clears maintenance
			// state accumulated from previous session crash loops.
			resetPlatformServices(*httpAddr, platformServiceIDs)
			fmt.Println("Starting platform services...")
			return forEachProject(*socketPath, platformProjects,
				daemon.CmdProjectStart, "started", "already running")
		},
	}
}

// resetPlatformServices calls POST /services/:id/reset for each service.
// Best-effort — 404s (service not yet registered) are silently ignored.
func resetPlatformServices(httpAddr string, serviceIDs []string) {
	for _, id := range serviceIDs {
		resp, err := http.Post(
			httpAddr+"/services/"+id+"/reset",
			"application/json", nil)
		if err != nil || resp == nil {
			continue
		}
		resp.Body.Close()
	}
}

func platformStopCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop all platform services",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Stopping platform services...")
			return forEachProject(*socketPath, platformProjects,
				daemon.CmdProjectStop, "stopped", "already stopped")
		},
	}
}

func platformStatusCmd(socketPath, httpAddr *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show status of all platform services",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := http.Get(*httpAddr + "/services")
			if err != nil {
				return fmt.Errorf("cannot reach engxd at %s: %w", *httpAddr, err)
			}
			defer resp.Body.Close()
			var result struct {
				OK   bool `json:"ok"`
				Data []struct {
					ID           string `json:"id"`
					Name         string `json:"name"`
					Project      string `json:"project"`
					DesiredState string `json:"desired_state"`
					ActualState  string `json:"actual_state"`
					FailCount    int    `json:"fail_count"`
				} `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("decode services: %w", err)
			}
			printPlatformStatus(result.Data)
			return nil
		},
	}
}

// printPlatformStatus prints a compact health summary for all platform services.
func printPlatformStatus(services []struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Project      string `json:"project"`
	DesiredState string `json:"desired_state"`
	ActualState  string `json:"actual_state"`
	FailCount    int    `json:"fail_count"`
}) {
	total, running := 0, 0
	for _, svc := range services {
		total++
		if svc.ActualState == "running" {
			running++
		}
	}
	fmt.Printf("Platform: %d/%d services running\n", running, total)
	for _, svc := range services {
		indicator := "●"
		if svc.ActualState != "running" {
			indicator = "○"
		}
		fmt.Printf("  %s %-20s desired=%-12s actual=%s\n",
			indicator, svc.Name, svc.DesiredState, svc.ActualState)
	}
}

// forEachProject sends a lifecycle command to a list of projects.
func forEachProject(socketPath string, projects []string, cmd daemon.Command, verb, alreadyMsg string) error {
	for _, proj := range projects {
		var params any
		if cmd == daemon.CmdProjectStart {
			params = daemon.ProjectStartParams{ProjectID: proj}
		} else {
			params = daemon.ProjectStopParams{ProjectID: proj}
		}
		resp, err := sendCommand(socketPath, cmd, params)
		if err != nil {
			fmt.Printf("  ✗ %s: %v\n", proj, err)
			continue
		}
		var r map[string]any
		_ = json.Unmarshal(resp.Data, &r)
		if q, _ := r["queued"].(float64); int(q) == 0 {
			fmt.Printf("  ○ %s: %s\n", proj, alreadyMsg)
		} else {
			fmt.Printf("  ✓ %s: %s (%d service)\n", proj, verb, int(q))
		}
	}
	return nil
}

// ── DOCTOR COMMAND ────────────────────────────────────────────────────────────

// doctorCmd aggregates health data from all platform observers and prints
// a single human-readable diagnosis. Read-only — calls no write endpoints.
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
	errors   []string
}

type doctorService struct {
	Name         string `json:"name"`
	DesiredState string `json:"desired_state"`
	ActualState  string `json:"actual_state"`
	FailCount    int    `json:"fail_count"`
}
type doctorAgent struct {
	ID      string `json:"id"`
	Online  bool   `json:"online"`
	LastSeen string `json:"last_seen"`
}
type doctorGuardian struct {
	Total    int
	Warnings int
	Errors   int
	Findings []struct {
		RuleID  string `json:"rule_id"`
		Target  string `json:"target"`
		Message string `json:"message"`
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

// collect fetches all signals concurrently and stores results.
func (d *doctorReport) collect() {
	fetchServices(d)
	fetchAgents(d)
	fetchGuardian(d)
	fetchSentinel(d)
	fetchMetrics(d)
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
			Summary  struct {
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
	printForge(d)
	printFetchErrors(d)
	fmt.Println()
	printSuggestions(d)
	fmt.Println()
}

func printDaemon(d *doctorReport) {
	uptime := formatUptime(d.metrics.UptimeSeconds)
	if d.metrics.UptimeSeconds > 0 {
		fmt.Printf("  ✓ engxd running       uptime %s\n", uptime)
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
			continue // nexus-daemon — expected
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
		if s.DesiredState == "stopped" {
			continue
		}
		if s.ActualState != "running" {
			fmt.Printf("    ✗ %-22s actual=%-12s fails=%d\n",
				s.Name, s.ActualState, s.FailCount)
		}
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

func printForge(d *doctorReport) {
	if d.metrics.TotalExecutions == 0 {
		fmt.Println("  ✓ forge               no executions yet")
		return
	}
	fmt.Printf("  ✓ forge               %d execution(s) total\n",
		d.metrics.TotalExecutions)
	if d.metrics.RecentCrashes > 0 {
		fmt.Printf("  ○ recent crashes      %d in last 10 minutes\n",
			d.metrics.RecentCrashes)
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
	// not-running services
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
	// engxa offline
	connected := false
	for _, a := range d.agents {
		if a.Online {
			connected = true
		}
	}
	if !connected && len(d.agents) > 0 {
		out = append(out, "restart engxa: /tmp/bin/engxa --id local --server http://127.0.0.1:8080 --token local-agent-token --addr 127.0.0.1:9090")
	}
	if len(d.agents) == 0 {
		out = append(out, "start engxa — services won't start without an agent")
	}
	// guardian findings
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

// ── DOCTOR HELPERS ────────────────────────────────────────────────────────────

func getJSON(url string, out any) error {
	return getJSONWithToken(url, "", out)
}

func getJSONWithToken(url, token string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set(canon.ServiceTokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func formatUptime(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%.0fs", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%.0fm", seconds/60)
	}
	h := int(seconds) / 3600
	m := (int(seconds) % 3600) / 60
	return fmt.Sprintf("%dh %dm", h, m)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ── LOGS COMMAND ──────────────────────────────────────────────────────────────

// logsCmd tails the process provider log for a service.
func logsCmd() *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "logs <service-id>",
		Short: "Tail the log for a platform service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("home dir: %w", err)
			}
			logPath := filepath.Join(home, ".nexus", "logs", id+".log")
			data, err := os.ReadFile(logPath)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("no log for %q — has the service started?", id)
				}
				return fmt.Errorf("read log: %w", err)
			}
			return printLastLines(string(data), lines)
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 40, "number of lines to show")
	return cmd
}

// printLastLines prints the last n lines of a string.
func printLastLines(s string, n int) error {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	start := 0
	if len(all) > n {
		start = len(all) - n
	}
	for _, line := range all[start:] {
		fmt.Println(line)
	}
	return nil
}

// ── BUILD COMMAND ─────────────────────────────────────────────────────────────

// buildCmd calls POST /commands on Forge with intent=build.
// Shows build output and errors directly in the terminal.
func buildCmd(httpAddr *string) *cobra.Command {
	var forgeAddr, lang, projectPath string
	cmd := &cobra.Command{
		Use:   "build <project-id>",
		Short: "Build a project via Forge",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			// Auto-resolve path and language from .nexus.yaml if not provided.
			if projectPath == "" || lang == "" {
				if resolved := resolveProjectMeta(target, projectPath, lang); resolved != nil {
					if projectPath == "" {
						projectPath = resolved.dir
					}
					if lang == "" {
						lang = resolved.language
					}
				}
			}
			fmt.Printf("Building %s...\n", target)
			result, err := forgeCommand(forgeAddr, "build", target, lang, projectPath)
			if err != nil {
				return err
			}
			return printForgeResult(result)
		},
	}
	cmd.Flags().StringVar(&forgeAddr, "forge", "http://127.0.0.1:8082",
		"Forge HTTP address")
	cmd.Flags().StringVarP(&lang, "language", "l", "",
		"project language (go, python, node, rust) — auto-detected if omitted")
	cmd.Flags().StringVar(&projectPath, "path", "",
		"project path — auto-resolved from .nexus.yaml if omitted")
	return cmd
}

// ── TRACE COMMAND ─────────────────────────────────────────────────────────────

// traceCmd surfaces trace timelines from Observer (ADR-014).
func traceCmd() *cobra.Command {
	var observerAddr string
	cmd := &cobra.Command{
		Use:   "trace [trace-id]",
		Short: "Show trace timeline — omit ID to list recent traces",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return traceList(observerAddr)
			}
			return traceShow(observerAddr, args[0])
		},
	}
	cmd.Flags().StringVar(&observerAddr, "observer", "http://127.0.0.1:8086",
		"Observer HTTP address")
	return cmd
}

// traceList prints the 50 most recent trace IDs from Observer.
func traceList(addr string) error {
	var result struct {
		OK   bool `json:"ok"`
		Data struct {
			Traces []struct {
				TraceID    string    `json:"trace_id"`
				FirstSeen  time.Time `json:"first_seen"`
				EventCount int       `json:"event_count"`
			} `json:"traces"`
		} `json:"data"`
	}
	if err := getJSON(addr+"/traces/recent", &result); err != nil {
		return fmt.Errorf("observer unavailable: %w", err)
	}
	if len(result.Data.Traces) == 0 {
		fmt.Println("No traces collected yet.")
		return nil
	}
	fmt.Printf("Recent traces (%d):\n", len(result.Data.Traces))
	for _, t := range result.Data.Traces {
		fmt.Printf("  %s  events=%-3d  first=%s\n",
			t.TraceID, t.EventCount, t.FirstSeen.Format("15:04:05"))
	}
	fmt.Println()
	fmt.Println("  Run: engx trace <trace-id>  to see the full timeline")
	return nil
}

// traceShow prints the full correlated timeline for one trace ID.
func traceShow(addr, traceID string) error {
	var result struct {
		OK   bool `json:"ok"`
		Data struct {
			TraceID  string `json:"trace_id"`
			Summary  struct {
				DurationMS     int64 `json:"duration_ms"`
				EventCount     int   `json:"event_count"`
				ExecutionCount int   `json:"execution_count"`
			} `json:"summary"`
			Timeline []struct {
				At      time.Time `json:"at"`
				Source  string    `json:"source"`
				Type    string    `json:"type"`
				Outcome string    `json:"outcome"`
				Status  string    `json:"status"`
				Target  string    `json:"target"`
				Intent  string    `json:"intent"`
				Message string    `json:"message"`
			} `json:"timeline"`
		} `json:"data"`
	}
	if err := getJSON(addr+"/traces/"+traceID, &result); err != nil {
		return fmt.Errorf("observer unavailable: %w", err)
	}
	d := result.Data
	fmt.Printf("Trace: %s\n", d.TraceID)
	fmt.Printf("  events=%d  executions=%d  duration=%dms\n\n",
		d.Summary.EventCount, d.Summary.ExecutionCount, d.Summary.DurationMS)
	for i := range d.Timeline {
		e := d.Timeline[i]
		fmt.Printf("  %s [%-5s] %s\n",
			e.At.Format("15:04:05.000"), e.Source,
			formatTimelineEntry(e.Type, e.Target, e.Intent, e.Outcome, e.Status, e.Message))
	}
	if len(d.Timeline) == 0 {
		fmt.Println("  No entries — trace may have expired (Observer ring buffer: 50 traces max).")
	}
	return nil
}

// formatTimelineEntry builds a human-readable description of one timeline entry.
func formatTimelineEntry(typ, target, intent, outcome, status, message string) string {
	detail := typ
	if target != "" {
		detail += " → " + target
	}
	if intent != "" {
		detail += " (" + intent + ")"
	}
	if outcome != "" {
		detail += " " + outcome
	}
	if status != "" && status != outcome {
		detail += " " + status
	}
	if message != "" {
		detail += ": " + truncate(message, 60)
	}
	return detail
}

// resolveProjectMeta finds path and language for a project target by reading
// .nexus.yaml from the current directory or known workspace locations.
func resolveProjectMeta(target, hintPath, hintLang string) *projectInfo {
	candidates := []string{}
	if hintPath != "" {
		candidates = append(candidates, hintPath)
	}
	// Current directory
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	// ~/workspace/projects/apps/<target>
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, "workspace", "projects", "apps", target))
	}
	for _, dir := range candidates {
		nexusYAML := filepath.Join(dir, ".nexus.yaml")
		if !fileExists(nexusYAML) {
			continue
		}
		info, err := detectProject(dir)
		if err == nil && (info.id == target || info.name == target || hintPath == dir) {
			return info
		}
	}
	return nil
}

// forgeCommand submits a command to Forge and returns the result.
// Passes context.language and context.project_path directly so Forge
// doesn't need Atlas to resolve them — safe when Atlas is unavailable.
func forgeCommand(httpAddr, intent, target, language, projectPath string) (map[string]any, error) {
	payload := map[string]any{
		"intent": intent,
		"target": target,
	}
	if language != "" || projectPath != "" {
		ctx := map[string]string{}
		if language != "" {
			ctx["language"] = language
		}
		if projectPath != "" {
			ctx["project_path"] = projectPath
		}
		payload["context"] = ctx
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(
		httpAddr+"/commands",
		"application/json",
		strings.NewReader(string(body)),
	)
	if err != nil {
		return nil, fmt.Errorf("cannot reach forge at %s: %w", httpAddr, err)
	}
	defer resp.Body.Close()
	var result struct {
		OK    bool           `json:"ok"`
		Data  map[string]any `json:"data"`
		Error string         `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode forge response: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("forge: %s", result.Error)
	}
	return result.Data, nil
}

// printForgeResult formats a Forge ExecutionResult for the terminal.
func printForgeResult(r map[string]any) error {
	success, _ := r["success"].(bool)
	duration, _ := r["duration"].(string)
	output, _ := r["output"].(string)
	errMsg, _ := r["error"].(string)

	if success {
		fmt.Printf("✓ success in %s\n", duration)
		if output != "" {
			fmt.Println(output)
		}
		return nil
	}
	fmt.Printf("✗ failed in %s\n", duration)
	if errMsg != "" {
		fmt.Println(errMsg)
	}
	return fmt.Errorf("build failed")
}

// ── CHECK COMMAND ─────────────────────────────────────────────────────────────

// checkCmd aggregates Atlas capabilities + Guardian findings for one project.
// Answers: "is this project ready to build/run?"
func checkCmd(httpAddr *string) *cobra.Command {
	var atlasAddr, token string
	cmd := &cobra.Command{
		Use:   "check <project-id>",
		Short: "Check a project's health — capabilities, status, Guardian findings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			printProjectAtlas(atlasAddr, token, id)
			printProjectGuardian(id)
			return nil
		},
	}
	cmd.Flags().StringVar(&atlasAddr, "atlas", "http://127.0.0.1:8081",
		"Atlas HTTP address")
	cmd.Flags().StringVar(&token, "token", "",
		"X-Service-Token (if auth is enabled)")
	return cmd
}

// printProjectAtlas fetches and prints Atlas status for a project.
func printProjectAtlas(atlasAddr, token, id string) {
	var result struct {
		OK   bool `json:"ok"`
		Data struct {
			ID           string   `json:"id"`
			Status       string   `json:"status"`
			Language     string   `json:"language"`
			Capabilities []string `json:"capabilities"`
			DependsOn    []string `json:"depends_on"`
		} `json:"data"`
	}
	url := fmt.Sprintf("%s/workspace/project/%s", atlasAddr, id)
	if err := getJSONWithToken(url, token, &result); err != nil {
		fmt.Printf("  atlas: unavailable (%v)\n", err)
		return
	}
	icon := "✓"
	if result.Data.Status != "verified" {
		icon = "○"
	}
	fmt.Printf("%s atlas: %s  status=%s  language=%s\n",
		icon, id, result.Data.Status, result.Data.Language)
	if len(result.Data.Capabilities) > 0 {
		fmt.Printf("  capabilities: %s\n",
			strings.Join(result.Data.Capabilities, ", "))
	}
}

// printProjectGuardian prints Guardian findings for a specific target.
func printProjectGuardian(id string) {
	var result struct {
		Data struct {
			Findings []struct {
				RuleID   string `json:"rule_id"`
				Target   string `json:"target"`
				Message  string `json:"message"`
				Severity string `json:"severity"`
			} `json:"findings"`
			Summary struct {
				Total int `json:"total"`
			} `json:"summary"`
		} `json:"data"`
	}
	if err := getJSON("http://127.0.0.1:8085/guardian/findings", &result); err != nil {
		fmt.Printf("  guardian: unavailable\n")
		return
	}
	var mine []struct {
		RuleID  string
		Message string
	}
	for _, f := range result.Data.Findings {
		if f.Target == id {
			mine = append(mine, struct {
				RuleID  string
				Message string
			}{f.RuleID, f.Message})
		}
	}
	if len(mine) == 0 {
		fmt.Println("✓ guardian: no findings for this project")
		return
	}
	fmt.Printf("○ guardian: %d finding(s)\n", len(mine))
	for _, f := range mine {
		fmt.Printf("  [%s] %s\n", f.RuleID, truncate(f.Message, 70))
	}
}

// ── RUN COMMAND ───────────────────────────────────────────────────────────────

// runCmd starts a project via Nexus and optionally waits until running.
func runCmd(socketPath, httpAddr *string) *cobra.Command {
	var wait bool
	var timeout int
	cmd := &cobra.Command{
		Use:   "run <project-id>",
		Short: "Start a project and optionally wait until its services are running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			resp, err := sendCommand(*socketPath,
				daemon.CmdProjectStart,
				daemon.ProjectStartParams{ProjectID: id})
			if err != nil {
				return err
			}
			var r map[string]any
			_ = json.Unmarshal(resp.Data, &r)
			queued, _ := r["queued"].(float64)
			if int(queued) == 0 {
				fmt.Printf("✓ %s: already running\n", id)
				return nil
			}
			fmt.Printf("✓ %s: queued %d service(s)\n", id, int(queued))
			if !wait {
				return nil
			}
			return waitForProject(*httpAddr, id, timeout)
		},
	}
	cmd.Flags().BoolVarP(&wait, "wait", "w", false,
		"wait until all services are running")
	cmd.Flags().IntVarP(&timeout, "timeout", "t", 60,
		"timeout in seconds when --wait is set")
	return cmd
}

// waitForProject polls GET /services?project=<id> until all are running.
func waitForProject(httpAddr, id string, timeoutSecs int) error {
	deadline := time.Now().Add(time.Duration(timeoutSecs) * time.Second)
	fmt.Printf("Waiting for %s (timeout %ds)...\n", id, timeoutSecs)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		running, total, err := projectServiceStates(httpAddr, id)
		if err != nil {
			continue
		}
		fmt.Printf("  %d/%d running\n", running, total)
		if running == total && total > 0 {
			fmt.Printf("✓ %s: all services running\n", id)
			return nil
		}
	}
	return fmt.Errorf("timeout: %s not fully running after %ds", id, timeoutSecs)
}

// projectServiceStates returns (running, total) for a project's services.
func projectServiceStates(httpAddr, projectID string) (int, int, error) {
	var result struct {
		Data []struct {
			ActualState  string `json:"actual_state"`
			DesiredState string `json:"desired_state"`
		} `json:"data"`
	}
	url := fmt.Sprintf("%s/services?project=%s", httpAddr, projectID)
	if err := getJSON(url, &result); err != nil {
		return 0, 0, err
	}
	total, running := 0, 0
	for _, s := range result.Data {
		if s.DesiredState == "stopped" {
			continue
		}
		total++
		if s.ActualState == "running" {
			running++
		}
	}
	return running, total, nil
}

// ── INIT COMMAND ──────────────────────────────────────────────────────────────

// initCmd generates a .nexus.yaml for a user's project (ADR-025).
func initCmd(socketPath, httpAddr *string) *cobra.Command {
	var dryRun, autoRegister bool
	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Generate .nexus.yaml for a project — onboard any project to the platform",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectPath := "."
			if len(args) > 0 {
				projectPath = args[0]
			}
			absPath, err := filepath.Abs(projectPath)
			if err != nil {
				return fmt.Errorf("resolve path: %w", err)
			}
			return runInit(absPath, dryRun, autoRegister, *socketPath, *httpAddr)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"print generated .nexus.yaml without writing")
	cmd.Flags().BoolVar(&autoRegister, "register", false,
		"run engx register after writing .nexus.yaml")
	return cmd
}

// projectInfo holds detected project metadata.
type projectInfo struct {
	name     string
	id       string
	projType string
	language string
	command  string
	args     []string
	dir      string
}

// runInit detects, generates, and optionally writes .nexus.yaml.
func runInit(absPath string, dryRun, autoRegister bool, socketPath, httpAddr string) error {
	info, err := detectProject(absPath)
	if err != nil {
		return err
	}
	yaml := buildNexusYAML(info)
	if dryRun {
		fmt.Printf("# .nexus.yaml (dry-run — not written)\n%s", yaml)
		return nil
	}
	outPath := filepath.Join(absPath, ".nexus.yaml")
	if err := os.WriteFile(outPath, []byte(yaml), 0644); err != nil {
		return fmt.Errorf("write .nexus.yaml: %w", err)
	}
	fmt.Printf("✓ .nexus.yaml written: %s\n", outPath)
	fmt.Printf("  name=%s  id=%s  type=%s  language=%s\n",
		info.name, info.id, info.projType, info.language)
	if info.command != "" {
		fmt.Printf("  runtime: %s %s\n", info.command, strings.Join(info.args, " "))
	}
	fmt.Println()
	fmt.Println("  ○ For Atlas verification, create nexus.yaml with capabilities declared.")
	fmt.Println("    See: definitions/glossary.md#project")
	if autoRegister {
		fmt.Println()
		resp, err := sendCommand(socketPath, daemon.CmdRegisterProject,
			daemon.RegisterProjectParams{
				ID: info.id, Name: info.name, Path: absPath,
				Language: info.language, ProjectType: info.projType,
			})
		if err != nil {
			fmt.Printf("  ○ register skipped: %v\n", err)
		} else {
			var r map[string]string
			_ = json.Unmarshal(resp.Data, &r)
			fmt.Printf("  ✓ registered: %s\n", info.id)
		}
	}
	return nil
}

// detectProject auto-detects language, type, and runtime for a project dir.
func detectProject(absPath string) (*projectInfo, error) {
	name := filepath.Base(absPath)
	id := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	info := &projectInfo{name: name, id: id, dir: absPath}
	detectLanguageAndType(info, absPath)
	detectEntryPoint(info, absPath)
	return info, nil
}

// detectLanguageAndType identifies language and project type from manifest files.
func detectLanguageAndType(info *projectInfo, dir string) {
	switch {
	case fileExists(filepath.Join(dir, "go.mod")):
		info.language = "go"
		if hasGoCmd(dir) {
			info.projType = "platform-daemon"
		} else {
			info.projType = "library"
		}
	case fileExists(filepath.Join(dir, "package.json")):
		info.language = "node"
		info.projType = "web-api"
	case fileExists(filepath.Join(dir, "pyproject.toml")),
		fileExists(filepath.Join(dir, "requirements.txt")):
		info.language = "python"
		info.projType = "worker"
	case fileExists(filepath.Join(dir, "Cargo.toml")):
		info.language = "rust"
		info.projType = "cli"
	default:
		info.language = ""
		info.projType = "tool"
	}
}

// detectEntryPoint finds the command and args to run the project.
func detectEntryPoint(info *projectInfo, dir string) {
	switch info.language {
	case "go":
		detectGoEntryPoint(info, dir)
	case "node":
		info.command = "node"
		info.args = []string{"index.js"}
	case "python":
		if fileExists(filepath.Join(dir, "main.py")) {
			info.command = "python3"
			info.args = []string{"main.py"}
		} else if fileExists(filepath.Join(dir, "app.py")) {
			info.command = "python3"
			info.args = []string{"app.py"}
		}
	case "rust":
		info.command = "cargo"
		info.args = []string{"run"}
	}
}

// detectGoEntryPoint finds cmd/<n>/main.go or root main.go.
func detectGoEntryPoint(info *projectInfo, dir string) {
	cmdDir := filepath.Join(dir, "cmd")
	if entries, err := os.ReadDir(cmdDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				candidate := filepath.Join(cmdDir, e.Name(), "main.go")
				if fileExists(candidate) {
					info.command = "go"
					info.args = []string{"run", "./cmd/" + e.Name() + "/"}
					return
				}
			}
		}
	}
	if fileExists(filepath.Join(dir, "main.go")) {
		info.command = "go"
		info.args = []string{"run", "."}
	}
}

// buildNexusYAML generates the .nexus.yaml content from detected info.
func buildNexusYAML(info *projectInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", info.name)
	fmt.Fprintf(&b, "id: %s\n", info.id)
	fmt.Fprintf(&b, "type: %s\n", info.projType)
	fmt.Fprintf(&b, "language: %s\n", info.language)
	fmt.Fprintf(&b, "version: 1.0.0\n")
	fmt.Fprintf(&b, "keywords: []\n")
	fmt.Fprintf(&b, "capabilities: []\n")
	fmt.Fprintf(&b, "depends_on: []\n")
	if info.command != "" {
		fmt.Fprintf(&b, "runtime:\n")
		fmt.Fprintf(&b, "  provider: process\n")
		fmt.Fprintf(&b, "  command: %s\n", info.command)
		if len(info.args) > 0 {
			fmt.Fprintf(&b, "  args: [%s]\n",
				strings.Join(quotedArgs(info.args), ", "))
		}
		fmt.Fprintf(&b, "  dir: %s\n", info.dir)
	}
	return b.String()
}

// quotedArgs wraps args containing spaces in quotes.
func quotedArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.Contains(a, " ") {
			out[i] = `"` + a + `"`
		} else {
			out[i] = a
		}
	}
	return out
}

// fileExists returns true if the path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// hasGoCmd returns true if cmd/<n>/main.go exists in dir.
func hasGoCmd(dir string) bool {
	cmdDir := filepath.Join(dir, "cmd")
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		return fileExists(filepath.Join(dir, "main.go"))
	}
	for _, e := range entries {
		if e.IsDir() && fileExists(filepath.Join(cmdDir, e.Name(), "main.go")) {
			return true
		}
	}
	return false
}

func versionCmd() *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("engx version %s\n", cliVersion)
		},
	}
}
