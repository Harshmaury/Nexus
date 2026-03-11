// @nexus-project: nexus
// @nexus-path: cmd/engx/main.go
// engx is the Nexus CLI — the developer-facing interface to the daemon.
// It communicates with the state store directly for read operations,
// and sets desired state for write operations (daemon reconciles).
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/spf13/cobra"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	defaultDBPath = "~/.nexus/nexus.db"
	cliVersion    = "0.1.0"
)

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
Start, stop, and monitor projects and services from one place.

GitHub: https://github.com/Harshmaury/Nexus`,
		Version: cliVersion,
	}

	root.PersistentFlags().StringVar(
		&dbPath, "db", expandHome(defaultDBPath),
		"path to nexus state database",
	)

	root.AddCommand(
		projectCmd(&dbPath),
		servicesCmd(&dbPath),
		eventsCmd(&dbPath),
		versionCmd(),
	)

	return root
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
		Use:   "start <project-id>",
		Short: "Start all services in a project",
		Args:  cobra.ExactArgs(1),
		Example: `  engx project start ums
  engx project start nexus`,
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
			fmt.Printf("  Run 'engx project status %s' to monitor\n", projectID)
			return nil
		},
	}
}

func projectStopCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <project-id>",
		Short: "Stop all services in a project",
		Args:  cobra.ExactArgs(1),
		Example: `  engx project stop ums
  engx project stop nexus`,
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

			fmt.Printf("✓ Queued %d service(s) to stop — daemon will reconcile\n", count)
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
					fmt.Println(ctrl.RenderStatus(s))
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

			fmt.Println(ctrl.RenderStatus(status))
			return nil
		},
	}

	cmd.Flags().BoolVar(&showAll, "all", false, "show status for all registered projects")
	return cmd
}

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
			fmt.Println(repeatChar("─", 82))

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

			fmt.Printf("\n%-20s %-25s %-20s %-16s %s\n",
				"TIME", "TYPE", "SERVICE", "SOURCE", "TRACE")
			fmt.Println(repeatChar("─", 90))

			for _, e := range events {
				traceShort := e.TraceID
				if len(traceShort) > 16 {
					traceShort = traceShort[:16] + "…"
				}
				fmt.Printf("%-20s %-25s %-20s %-16s %s\n",
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
			fmt.Printf("github.com/Harshmaury/Nexus\n")
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
	path := expandHome(*dbPath)

	// Ensure directory exists
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

func expandHome(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func repeatChar(char string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += char
	}
	return result
}
