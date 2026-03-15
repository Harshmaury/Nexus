// @nexus-project: nexus
// @nexus-path: cmd/engx/main.go
// engx is the Nexus CLI — the developer-facing interface to the daemon.
//
// Phase 10 additions:
//   - engx drop pending          → list all files awaiting approval
//   - engx drop approve <file>   → move file to resolved destination
//   - engx drop reject  <file>   → tag file UNROUTED__ and leave in place
//
//   The <file> argument accepts either the full absolute path or just the
//   filename — the CLI resolves basename matches from the pending list so
//   the user never has to type the full path.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
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
		versionCmd(),
	)

	return root
}

// ── SOCKET CLIENT ─────────────────────────────────────────────────────────────

func sendCommand(socketPath string, cmd daemon.Command, params any) (*daemon.Response, error) {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf(
			"cannot connect to Nexus daemon at %s\n  Is engxd running? Start it with: engxd",
			socketPath,
		)
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

// ── DROP COMMAND ──────────────────────────────────────────────────────────────

func dropCmd(socketPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "drop",
		Short: "Manage files pending approval in the drop folder",
		Example: `  engx drop pending
  engx drop approve my-file.go
  engx drop reject  my-file.go`,
	}

	cmd.AddCommand(
		dropPendingCmd(socketPath),
		dropApproveCmd(socketPath),
		dropRejectCmd(socketPath),
	)

	return cmd
}

// dropPendingCmd lists all files currently awaiting approval.
func dropPendingCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "pending",
		Short: "List files waiting for approve or reject",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdDropPending, nil)
			if err != nil {
				return err
			}

			var entries []daemon.PendingApproval
			if err := json.Unmarshal(resp.Data, &entries); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}

			if len(entries) == 0 {
				fmt.Println("No files pending approval.")
				return nil
			}

			fmt.Printf("\n%-40s %-20s %-8s  %s\n", "FILE", "PROJECT", "CONF", "DESTINATION")
			fmt.Println(strings.Repeat("─", 100))

			for _, e := range entries {
				fmt.Printf("%-40s %-20s %5.0f%%   %s\n",
					e.FileName,
					e.ProjectID,
					e.Confidence*100,
					e.Destination,
				)
			}

			fmt.Printf("\n%d file(s) pending — use: engx drop approve <file> | engx drop reject <file>\n\n",
				len(entries))
			return nil
		},
	}
}

// dropApproveCmd approves a pending file — moves it to its resolved destination.
func dropApproveCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:     "approve <file>",
		Short:   "Approve a pending file — move it to its resolved destination",
		Args:    cobra.ExactArgs(1),
		Example: `  engx drop approve my-file.go`,
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

			fmt.Printf("\n\033[32m✓\033[0m Approved: %s\n", result["file"])
			fmt.Printf("  Project:     %s\n", result["project"])
			fmt.Printf("  Destination: %s\n\n", result["destination"])
			return nil
		},
	}
}

// dropRejectCmd rejects a pending file — tags it UNROUTED__ and leaves it in place.
func dropRejectCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:     "reject <file>",
		Short:   "Reject a pending file — tag it UNROUTED__ and leave in drop folder",
		Args:    cobra.ExactArgs(1),
		Example: `  engx drop reject my-file.go`,
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

			fmt.Printf("\n\033[31m✗\033[0m Rejected: %s\n", result["file"])
			fmt.Printf("  Tagged as:  %s\n\n", result["tagged"])
			return nil
		},
	}
}

// resolvePendingFile resolves a filename or path argument to the full absolute
// path stored in the daemon's pending map.
// Accepts: exact absolute path, or basename (matched against pending list).
func resolvePendingFile(socketPath, arg string) (string, error) {
	// If arg is an absolute path, use directly.
	if filepath.IsAbs(arg) {
		return arg, nil
	}

	// Otherwise fetch the pending list and match by basename.
	resp, err := sendCommand(socketPath, daemon.CmdDropPending, nil)
	if err != nil {
		return "", fmt.Errorf("fetch pending list: %w", err)
	}

	var entries []daemon.PendingApproval
	if err := json.Unmarshal(resp.Data, &entries); err != nil {
		return "", fmt.Errorf("decode pending list: %w", err)
	}

	var matches []daemon.PendingApproval
	for _, e := range entries {
		if e.FileName == arg || strings.EqualFold(e.FileName, arg) {
			matches = append(matches, e)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no pending file named %q — run 'engx drop pending' to list", arg)
	case 1:
		return matches[0].FilePath, nil
	default:
		// Multiple matches — print them and ask for full path.
		fmt.Fprintf(os.Stderr, "multiple pending files named %q:\n", arg)
		for _, m := range matches {
			fmt.Fprintf(os.Stderr, "  %s\n", m.FilePath)
		}
		return "", fmt.Errorf("use the full path to disambiguate")
	}
}

// ── REGISTER COMMAND ──────────────────────────────────────────────────────────

func registerCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:     "register <project-path>",
		Short:   "Register a project with Nexus",
		Args:    cobra.ExactArgs(1),
		Example: `  engx register ~/workspace/projects/apps/nexus`,
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
				ID:          manifest.id,
				Name:        manifest.name,
				Path:        projectPath,
				Language:    manifest.language,
				ProjectType: manifest.projectType,
				ConfigJSON:  manifest.rawYAML,
			})
			if err != nil {
				return err
			}

			var result map[string]string
			_ = json.Unmarshal(resp.Data, &result)

			fmt.Printf("✓ Registered: %s (id: %s)\n", manifest.name, manifest.id)
			fmt.Printf("  Run: engx project status %s\n", manifest.id)
			return nil
		},
	}
}

type nexusManifest struct {
	id          string
	name        string
	language    string
	projectType string
	rawYAML     string
}

func readNexusManifest(projectPath string) (*nexusManifest, error) {
	manifestPath := filepath.Join(projectPath, ".nexus.yaml")
	file, err := os.Open(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(".nexus.yaml not found in %s", projectPath)
		}
		return nil, fmt.Errorf("open .nexus.yaml: %w", err)
	}
	defer file.Close()

	manifest := &nexusManifest{}
	var rawLines []string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		rawLines = append(rawLines, line)
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "name":
			manifest.name = value
			manifest.id = strings.ToLower(strings.ReplaceAll(value, " ", "-"))
		case "id":
			manifest.id = value
		case "language":
			manifest.language = value
		case "type":
			manifest.projectType = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read .nexus.yaml: %w", err)
	}
	if manifest.name == "" {
		return nil, fmt.Errorf(".nexus.yaml missing required field: name")
	}
	manifest.rawYAML = strings.Join(rawLines, "\n")
	return manifest, nil
}

// ── PROJECT COMMANDS ──────────────────────────────────────────────────────────

func projectCmd(socketPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage projects",
	}
	cmd.AddCommand(projectStartCmd(socketPath), projectStopCmd(socketPath), projectStatusCmd(socketPath))
	return cmd
}

func projectStartCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:  "start <project-id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdProjectStart,
				daemon.ProjectStartParams{ProjectID: args[0]})
			if err != nil {
				return err
			}
			var result map[string]any
			_ = json.Unmarshal(resp.Data, &result)
			queued, _ := result["queued"].(float64)
			if int(queued) == 0 {
				fmt.Printf("✓ All services in %q already running\n", args[0])
				return nil
			}
			fmt.Printf("✓ Queued %d service(s) to start\n  Run: engx project status %s\n",
				int(queued), args[0])
			return nil
		},
	}
}

func projectStopCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:  "stop <project-id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdProjectStop,
				daemon.ProjectStopParams{ProjectID: args[0]})
			if err != nil {
				return err
			}
			var result map[string]any
			_ = json.Unmarshal(resp.Data, &result)
			queued, _ := result["queued"].(float64)
			if int(queued) == 0 {
				fmt.Printf("✓ All services in %q already stopped\n", args[0])
				return nil
			}
			fmt.Printf("✓ Queued %d service(s) to stop\n", int(queued))
			return nil
		},
	}
}

func projectStatusCmd(socketPath *string) *cobra.Command {
	var showAll bool
	cmd := &cobra.Command{
		Use:  "status [project-id]",
		Args: cobra.MaximumNArgs(1),
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
				var statuses []*controllers.ProjectStatus
				if err := json.Unmarshal(resp.Data, &statuses); err != nil {
					return fmt.Errorf("decode: %w", err)
				}
				if len(statuses) == 0 {
					fmt.Println("No projects registered.")
					return nil
				}
				for _, s := range statuses {
					fmt.Print(renderStatus(s))
				}
				return nil
			}
			var status controllers.ProjectStatus
			if err := json.Unmarshal(resp.Data, &status); err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			fmt.Print(renderStatus(&status))
			return nil
		},
	}
	cmd.Flags().BoolVar(&showAll, "all", false, "show all projects")
	return cmd
}

func renderStatus(status *controllers.ProjectStatus) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\nPROJECT: %s (%s)\n", strings.ToUpper(status.ProjectID), status.ProjectName))
	sb.WriteString(fmt.Sprintf("%-30s %-12s %-12s %-10s %s\n", "SERVICE", "DESIRED", "ACTUAL", "PROVIDER", "HEALTH"))
	sb.WriteString(strings.Repeat("─", 78) + "\n")
	healthy := 0
	for _, svc := range status.Services {
		indicator := "\033[32m✓\033[0m"
		if !svc.IsHealthy {
			indicator = "\033[31m✗\033[0m"
		}
		if svc.FailCount > 0 {
			indicator += fmt.Sprintf(" (%d failures)", svc.FailCount)
		}
		sb.WriteString(fmt.Sprintf("%-30s %-12s %-12s %-10s %s\n",
			svc.Name, string(svc.DesiredState), string(svc.ActualState), string(svc.Provider), indicator))
		if svc.IsHealthy {
			healthy++
		}
	}
	total := len(status.Services)
	summary := fmt.Sprintf("\n%d/%d services healthy", healthy, total)
	if healthy == total {
		sb.WriteString("\033[32m" + summary + "\033[0m\n")
	} else {
		sb.WriteString("\033[31m" + summary + "\033[0m\n")
	}
	return sb.String()
}

// ── SERVICES COMMAND ──────────────────────────────────────────────────────────

func servicesCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "services",
		Short: "List all registered services",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdServicesList, nil)
			if err != nil {
				return err
			}
			var services []*state.Service
			if err := json.Unmarshal(resp.Data, &services); err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			if len(services) == 0 {
				fmt.Println("No services registered.")
				return nil
			}
			fmt.Printf("\n%-30s %-15s %-12s %-12s %-10s\n", "SERVICE", "PROJECT", "DESIRED", "ACTUAL", "PROVIDER")
			fmt.Println(strings.Repeat("─", 82))
			for _, svc := range services {
				fmt.Printf("%-30s %-15s %-12s %-12s %-10s\n",
					svc.Name, svc.Project, string(svc.DesiredState), string(svc.ActualState), string(svc.Provider))
			}
			fmt.Printf("\nTotal: %d services\n\n", len(services))
			return nil
		},
	}
}

// ── EVENTS COMMAND ────────────────────────────────────────────────────────────

func eventsCmd(socketPath *string) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show recent platform events",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendCommand(*socketPath, daemon.CmdEventsList, daemon.EventsListParams{Limit: limit})
			if err != nil {
				return err
			}
			var events []*state.Event
			if err := json.Unmarshal(resp.Data, &events); err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			if len(events) == 0 {
				fmt.Println("No events recorded yet.")
				return nil
			}
			fmt.Printf("\n%-20s %-25s %-20s %s\n", "TIME", "TYPE", "SERVICE", "TRACE")
			fmt.Println(strings.Repeat("─", 90))
			for _, e := range events {
				trace := e.TraceID
				if len(trace) > 20 {
					trace = trace[:20] + "…"
				}
				fmt.Printf("%-20s %-25s %-20s %s\n",
					e.CreatedAt.Format("01-02 15:04:05"), string(e.Type), e.ServiceID, trace)
			}
			fmt.Printf("\nShowing %d events\n\n", len(events))
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "number of events")
	return cmd
}

// ── VERSION ───────────────────────────────────────────────────────────────────

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("engx version %s\n", cliVersion)
		},
	}
}
