// @nexus-project: nexus
// @nexus-path: cmd/engx/main.go
// engx is the Nexus CLI — the developer-facing interface to the daemon.
// It communicates with the state store directly for read operations,
// and sets desired state for write operations (daemon reconciles).
//
// Changes from previous version:
//   - Removed local expandHome() — now uses config.ExpandHome
//   - Removed local defaultDBPath constant — now uses config.DefaultDBPath
//   - Added import of internal/config
//   - Removed unused "path/filepath" and "os" (expandHome needed them)
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Harshmaury/Nexus/internal/config"
	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/spf13/cobra"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const cliVersion = "0.1.0"

// ── ENTRY POINT ──────────────────────────────────────────────────────────────

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// ── ROOT COMMAND ─────────────────────────────────────────────────────────────

func rootCmd() *cobra.Command {
	var dbPath string

	root := &cobra.Command{
		Use:   "engx",
		Short: "Nexus — Local Developer Control Plane",
		Long: `engx controls your entire local developer environment.
Start, stop, monitor, and register projects from one place.

GitHub: https://github.com/Harshmaury/Nexus`,
		Version: cliVersion,
	}

	root.PersistentFlags().StringVar(
		&dbPath, "db", config.ExpandHome(config.DefaultDBPath),
		"path to nexus state database",
	)

	root.AddCommand(
		projectCmd(&dbPath),
		registerCmd(&dbPath),
		servicesCmd(&dbPath),
		eventsCmd(&dbPath),
		versionCmd(),
	)

	return root
}

// ── REGISTER COMMAND ─────────────────────────────────────────────────────────

func registerCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "register <project-path>",
		Short: "Register a project with Nexus",
		Long: `Register reads .nexus.yaml from the project root and adds
the project to the Nexus state database.

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

			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			project := &state.Project{
				ID:          manifest.id,
				Name:        manifest.name,
				Path:        projectPath,
				Language:    manifest.language,
				ProjectType: manifest.projectType,
				ConfigJSON:  manifest.rawYAML,
			}

			if err := store.RegisterProject(project); err != nil {
				return fmt.Errorf("register project: %w", err)
			}

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
// We avoid a YAML dependency — the format is intentionally minimal.
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

// ── PROJECT COMMANDS ─────────────────────────────────────────────────────────

func projectCmd(dbPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage entire projects as a unit",
		Example: `  engx project start ums
  engx project stop ums
  engx project status ums
  engx project status --all`,
	}

	cmd.AddCommand(
		projectStartCmd(dbPath),
		projectStopCmd(dbPath),
		projectStatusCmd(dbPath),
	)

	return cmd
}

func projectStartCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:     "start <project-id>",
		Short:   "Start all services in a project",
		Args:    cobra.ExactArgs(1),
		Example: `  engx project start ums`,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := args[0]

			ctrl, cleanup, err := buildProjectController(dbPath)
			if err != nil {
				return err
			}
			defer cleanup()

			fmt.Printf("Starting project: %s\n", projectID)

			count, err := ctrl.StartProject(projectID)
			if err != nil {
				return fmt.Errorf("start project: %w", err)
			}

			if count == 0 {
				fmt.Printf("✓ All services in %q already running\n", projectID)
				return nil
			}

			fmt.Printf("✓ Queued %d service(s) to start — daemon will reconcile\n", count)
			fmt.Printf("  Run: engx project status %s\n", projectID)
			return nil
		},
	}
}

func projectStopCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:     "stop <project-id>",
		Short:   "Stop all services in a project",
		Args:    cobra.ExactArgs(1),
		Example: `  engx project stop ums`,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := args[0]

			ctrl, cleanup, err := buildProjectController(dbPath)
			if err != nil {
				return err
			}
			defer cleanup()

			fmt.Printf("Stopping project: %s\n", projectID)

			count, err := ctrl.StopProject(projectID)
			if err != nil {
				return fmt.Errorf("stop project: %w", err)
			}

			if count == 0 {
				fmt.Printf("✓ All services in %q already stopped\n", projectID)
				return nil
			}

			fmt.Printf("✓ Queued %d service(s) to stop\n", count)
			return nil
		},
	}
}

func projectStatusCmd(dbPath *string) *cobra.Command {
	var showAll bool

	cmd := &cobra.Command{
		Use:   "status [project-id]",
		Short: "Show health status of a project or all projects",
		Args:  cobra.MaximumNArgs(1),
		Example: `  engx project status ums
  engx project status --all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctrl, cleanup, err := buildProjectController(dbPath)
			if err != nil {
				return err
			}
			defer cleanup()

			if showAll {
				statuses, err := ctrl.GetAllProjectsStatus()
				if err != nil {
					return fmt.Errorf("get all project statuses: %w", err)
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

			if len(args) == 0 {
				return fmt.Errorf("provide a project ID or use --all flag")
			}

			status, err := ctrl.GetProjectStatus(args[0])
			if err != nil {
				return fmt.Errorf("get project status: %w", err)
			}

			fmt.Print(renderStatus(status))
			return nil
		},
	}

	cmd.Flags().BoolVar(&showAll, "all", false, "show status for all registered projects")
	return cmd
}

// ── RENDER STATUS ────────────────────────────────────────────────────────────

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

// ── SERVICES COMMAND ─────────────────────────────────────────────────────────

func servicesCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "services",
		Short: "List all registered services",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			services, err := store.GetAllServices()
			if err != nil {
				return fmt.Errorf("get services: %w", err)
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

// ── EVENTS COMMAND ───────────────────────────────────────────────────────────

func eventsCmd(dbPath *string) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show recent platform events",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			events, err := store.GetRecentEvents(limit)
			if err != nil {
				return fmt.Errorf("get events: %w", err)
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

// ── VERSION COMMAND ──────────────────────────────────────────────────────────

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

// ── HELPERS ──────────────────────────────────────────────────────────────────

func buildProjectController(dbPath *string) (*controllers.ProjectController, func(), error) {
	store, err := openStore(dbPath)
	if err != nil {
		return nil, nil, err
	}
	bus := eventbus.New()
	ctrl := controllers.NewProjectController(store, bus)
	cleanup := func() { store.Close() }
	return ctrl, cleanup, nil
}

func openStore(dbPath *string) (*state.Store, error) {
	path := config.ExpandHome(*dbPath) // uses config.ExpandHome — no local duplicate

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create database directory %s: %w", dir, err)
	}

	store, err := state.New(path)
	if err != nil {
		return nil, fmt.Errorf("open state store at %s: %w", path, err)
	}
	return store, nil
}
