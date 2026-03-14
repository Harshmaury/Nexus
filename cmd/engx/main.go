// @nexus-project: nexus
// @nexus-path: cmd/engx/main.go
// engx is the Nexus CLI — the developer-facing interface to the daemon.
//
// Fix 03 changes (retained from previous version):
//   - Removed openStore, buildProjectController, and all direct SQLite access.
//   - CLI is now a pure thin socket client: every command sends a JSON request
//     to the daemon over the Unix socket and renders the response.
//   - register command now sends CmdRegisterProject to daemon (single writer).
//   - --db flag removed (CLI no longer opens the database).
//   - --socket flag added to override the Unix socket path.
//
// Fix (this version):
//   - Removed dead `config` import. The CLI is a pure socket client and has
//     no use for the config package. The previous `var _ = config.DefaultDBPath`
//     sentinel existed only to suppress the unused import compiler error.
//     Both lines removed.
//
// Data flow (all commands):
//   engx → Unix socket → engxd dispatcher → controller/store → response → engx
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

// ── ENTRY POINT ───────────────────────────────────────────────────────────────

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// ── ROOT COMMAND ──────────────────────────────────────────────────────────────

func rootCmd() *cobra.Command {
	var socketPath string

	root := &cobra.Command{
		Use:   "engx",
		Short: "Nexus — Local Developer Control Plane",
		Long: `engx controls your entire local developer environment.
Start, stop, monitor, and register projects from one place.

GitHub: https://github.com/Harshmaury/Nexus`,
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
		versionCmd(),
	)

	return root
}

// ── SOCKET CLIENT ─────────────────────────────────────────────────────────────

// sendCommand opens a connection to the daemon socket, sends a request,
// and returns the parsed response. All CLI commands use this — no direct DB.
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

// ── REGISTER COMMAND ──────────────────────────────────────────────────────────

func registerCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "register <project-path>",
		Short: "Register a project with Nexus",
		Long: `Register reads .nexus.yaml from the project root and sends
the project metadata to the daemon, which writes it to the state store.

Example .nexus.yaml:
  name: my-project
  type: web-api
  language: go`,
		Args:    cobra.ExactArgs(1),
		Example: `  engx register ~/dev/projects/ums
  engx register .`,
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

			fmt.Printf("✓ Registered project: %s\n", manifest.name)
			fmt.Printf("  ID:       %s\n", manifest.id)
			fmt.Printf("  Path:     %s\n", projectPath)
			fmt.Printf("  Language: %s\n", manifest.language)
			fmt.Printf("  Type:     %s\n", manifest.projectType)
			fmt.Printf("\n  Run 'engx project status %s' to check services\n", manifest.id)
			return nil
		},
	}
}

// nexusManifest holds parsed .nexus.yaml fields.
type nexusManifest struct {
	id          string
	name        string
	language    string
	projectType string
	rawYAML     string
}

// readNexusManifest parses .nexus.yaml with a simple line scanner.
func readNexusManifest(projectPath string) (*nexusManifest, error) {
	manifestPath := filepath.Join(projectPath, ".nexus.yaml")

	file, err := os.Open(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(
				".nexus.yaml not found in %s\n\nCreate one:\n  name: my-project\n  type: web-api\n  language: go",
				projectPath,
			)
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
		return nil, fmt.Errorf(".nexus.yaml is missing required field: name")
	}
	if manifest.id == "" {
		return nil, fmt.Errorf(".nexus.yaml is missing required field: name or id")
	}

	manifest.rawYAML = strings.Join(rawLines, "\n")
	return manifest, nil
}

// ── PROJECT COMMANDS ──────────────────────────────────────────────────────────

func projectCmd(socketPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage entire projects as a unit",
		Example: `  engx project start ums
  engx project stop ums
  engx project status ums
  engx project status --all`,
	}

	cmd.AddCommand(
		projectStartCmd(socketPath),
		projectStopCmd(socketPath),
		projectStatusCmd(socketPath),
	)

	return cmd
}

func projectStartCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:     "start <project-id>",
		Short:   "Start all services in a project",
		Args:    cobra.ExactArgs(1),
		Example: `  engx project start ums`,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := args[0]
			fmt.Printf("Starting project: %s\n", projectID)

			resp, err := sendCommand(*socketPath, daemon.CmdProjectStart,
				daemon.ProjectStartParams{ProjectID: projectID})
			if err != nil {
				return err
			}

			var result map[string]any
			_ = json.Unmarshal(resp.Data, &result)

			queued, _ := result["queued"].(float64)
			if int(queued) == 0 {
				fmt.Printf("✓ All services in %q already running\n", projectID)
				return nil
			}
			fmt.Printf("✓ Queued %d service(s) to start — daemon will reconcile\n", int(queued))
			fmt.Printf("  Run: engx project status %s\n", projectID)
			return nil
		},
	}
}

func projectStopCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:     "stop <project-id>",
		Short:   "Stop all services in a project",
		Args:    cobra.ExactArgs(1),
		Example: `  engx project stop ums`,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := args[0]
			fmt.Printf("Stopping project: %s\n", projectID)

			resp, err := sendCommand(*socketPath, daemon.CmdProjectStop,
				daemon.ProjectStopParams{ProjectID: projectID})
			if err != nil {
				return err
			}

			var result map[string]any
			_ = json.Unmarshal(resp.Data, &result)

			queued, _ := result["queued"].(float64)
			if int(queued) == 0 {
				fmt.Printf("✓ All services in %q already stopped\n", projectID)
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
		Use:   "status [project-id]",
		Short: "Show health status of a project or all projects",
		Args:  cobra.MaximumNArgs(1),
		Example: `  engx project status ums
  engx project status --all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := ""
			if len(args) > 0 {
				projectID = args[0]
			}

			if !showAll && projectID == "" {
				return fmt.Errorf("provide a project ID or use --all flag")
			}

			resp, err := sendCommand(*socketPath, daemon.CmdProjectStatus,
				daemon.ProjectStatusParams{ProjectID: projectID})
			if err != nil {
				return err
			}

			if showAll || projectID == "" {
				var statuses []*controllers.ProjectStatus
				if err := json.Unmarshal(resp.Data, &statuses); err != nil {
					return fmt.Errorf("decode response: %w", err)
				}
				if len(statuses) == 0 {
					fmt.Println("No projects registered. Run: engx register <path>")
					return nil
				}
				for _, s := range statuses {
					fmt.Print(renderStatus(s))
				}
				return nil
			}

			var status controllers.ProjectStatus
			if err := json.Unmarshal(resp.Data, &status); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			fmt.Print(renderStatus(&status))
			return nil
		},
	}

	cmd.Flags().BoolVar(&showAll, "all", false, "show status for all registered projects")
	return cmd
}

// ── RENDER STATUS ─────────────────────────────────────────────────────────────

func renderStatus(status *controllers.ProjectStatus) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("\nPROJECT: %s (%s)\n",
		strings.ToUpper(status.ProjectID), status.ProjectName))
	sb.WriteString(fmt.Sprintf("%-30s %-12s %-12s %-10s %s\n",
		"SERVICE", "DESIRED", "ACTUAL", "PROVIDER", "HEALTH"))
	sb.WriteString(strings.Repeat("─", 78) + "\n")

	healthy := 0
	for _, svc := range status.Services {
		indicator := colorGreen("✓")
		if !svc.IsHealthy {
			indicator = colorRed("✗")
		}
		if svc.FailCount > 0 {
			indicator += fmt.Sprintf("  (%d failures)", svc.FailCount)
		}

		sb.WriteString(fmt.Sprintf("%-30s %-12s %-12s %-10s %s\n",
			svc.Name,
			string(svc.DesiredState),
			string(svc.ActualState),
			string(svc.Provider),
			indicator,
		))

		if svc.IsHealthy {
			healthy++
		}
	}

	total := len(status.Services)
	summary := fmt.Sprintf("\n%d/%d services healthy", healthy, total)
	if healthy == total {
		sb.WriteString(colorGreen(summary) + "\n")
	} else {
		sb.WriteString(colorRed(summary) + "\n")
	}

	return sb.String()
}

func colorGreen(s string) string { return "\033[32m" + s + "\033[0m" }
func colorRed(s string) string   { return "\033[31m" + s + "\033[0m" }

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
				return fmt.Errorf("decode response: %w", err)
			}

			if len(services) == 0 {
				fmt.Println("No services registered.")
				return nil
			}

			fmt.Printf("\n%-30s %-15s %-12s %-12s %-10s\n",
				"SERVICE", "PROJECT", "DESIRED", "ACTUAL", "PROVIDER")
			fmt.Println(strings.Repeat("─", 82))

			for _, svc := range services {
				fmt.Printf("%-30s %-15s %-12s %-12s %-10s\n",
					svc.Name, svc.Project,
					string(svc.DesiredState),
					string(svc.ActualState),
					string(svc.Provider),
				)
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
			resp, err := sendCommand(*socketPath, daemon.CmdEventsList,
				daemon.EventsListParams{Limit: limit})
			if err != nil {
				return err
			}

			var events []*state.Event
			if err := json.Unmarshal(resp.Data, &events); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}

			if len(events) == 0 {
				fmt.Println("No events recorded yet.")
				return nil
			}

			fmt.Printf("\n%-20s %-25s %-20s %-18s %s\n",
				"TIME", "TYPE", "SERVICE", "SOURCE", "TRACE")
			fmt.Println(strings.Repeat("─", 92))

			for _, e := range events {
				traceShort := e.TraceID
				if len(traceShort) > 18 {
					traceShort = traceShort[:18] + "…"
				}
				fmt.Printf("%-20s %-25s %-20s %-18s %s\n",
					e.CreatedAt.Format("01-02 15:04:05"),
					string(e.Type),
					e.ServiceID,
					string(e.Source),
					traceShort,
				)
			}
			fmt.Printf("\nShowing %d events\n\n", len(events))
			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "number of events to show")
	return cmd
}

// ── VERSION COMMAND ───────────────────────────────────────────────────────────

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print Nexus version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("engx version %s\n", cliVersion)
			fmt.Println("github.com/Harshmaury/Nexus")
		},
	}
}
