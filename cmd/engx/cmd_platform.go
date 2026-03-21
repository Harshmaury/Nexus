// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_platform.go
// Platform commands — start/stop/status for the full service fleet.
// ADR-023: reset fail counts before start.
// ADR-032: preflight registration check + --register flag.
// F-4 fix: platformStatusCmd uses Herald instead of raw HTTP + anonymous struct.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	herald "github.com/Harshmaury/Herald/client"
	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/spf13/cobra"
)

// platformProjects is the ordered start sequence.
// Nexus is excluded — must already be running before engx can connect.
var platformProjects = []string{
	"atlas", "forge",
	"metrics", "navigator", "guardian", "observer", "sentinel",
}

// platformServiceIDs are the service IDs for reset before start (ADR-023).
var platformServiceIDs = []string{
	"atlas-daemon", "forge-daemon",
	"metrics-daemon", "navigator-daemon", "guardian-daemon",
	"observer-daemon", "sentinel-daemon",
}

func platformCmd(socketPath, httpAddr *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "platform",
		Short: "Control the full platform (start/stop/status all services)",
	}
	cmd.AddCommand(
		platformStartCmd(socketPath, httpAddr),
		platformStopCmd(socketPath),
		platformStatusCmd(socketPath, httpAddr),
		platformInstallCmd(),
		platformUninstallCmd(),
		platformServiceLogsCmd(),
	)
	return cmd
}

// platformStartCmd starts all platform services.
// ADR-023: resets fail counts first.
// ADR-032: checks registration, auto-registers with --register flag.
func platformStartCmd(socketPath, httpAddr *string) *cobra.Command {
	var autoRegister bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start all platform services",
		RunE: func(cmd *cobra.Command, args []string) error {
			// ADR-032: preflight registration check.
			missing := checkPlatformRegistered(*httpAddr, platformProjects)
			if len(missing) > 0 && !autoRegister {
				for _, m := range missing {
					fmt.Printf("  ✗ %s: not registered — run: engx register ~/workspace/projects/engx/services/%s\n", m, m)
				}
				fmt.Printf("\n  %d project(s) not registered.\n", len(missing))
				fmt.Printf("  Register them first, or run: engx platform start --register\n\n")
				return fmt.Errorf("platform not fully registered")
			}
			if autoRegister && len(missing) > 0 {
				fmt.Printf("Auto-registering %d missing project(s)...\n", len(missing))
				autoRegisterPlatform(*socketPath, *httpAddr, missing)
			}
			// ADR-023: reset before start.
			resetPlatformServices(*httpAddr, platformServiceIDs)
			fmt.Println("Starting platform services...")
			return forEachProject(*socketPath, platformProjects,
				daemon.CmdProjectStart, "started", "already running")
		},
	}
	cmd.Flags().BoolVar(&autoRegister, "register", false,
		"auto-register missing platform projects before starting")
	return cmd
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
			c := herald.New(*httpAddr)
			svcs, err := c.Services().List(context.Background())
			if err != nil {
				return fmt.Errorf("cannot reach engxd at %s: %w", *httpAddr, err)
			}
			total, running := 0, 0
			for _, svc := range svcs {
				total++
				if svc.ActualState == "running" {
					running++
				}
			}
			fmt.Printf("Platform: %d/%d services running\n", running, total)
			for _, svc := range svcs {
				ind := "●"
				if svc.ActualState != "running" {
					ind = "○"
				}
				fmt.Printf("  %s %-20s desired=%-12s actual=%s\n",
					ind, svc.Name, svc.DesiredState, svc.ActualState)
			}
			return nil
		},
	}
}

// ── ADR-032 HELPERS ───────────────────────────────────────────────────────────

// checkPlatformRegistered returns project IDs not yet registered in the daemon.
func checkPlatformRegistered(httpAddr string, projects []string) []string {
	var missing []string
	for _, id := range projects {
		resp, err := http.Get(httpAddr + "/projects/" + id)
		if err != nil || resp == nil || resp.StatusCode == http.StatusNotFound {
			missing = append(missing, id)
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	return missing
}

// autoRegisterPlatform registers missing projects from their default paths.
func autoRegisterPlatform(socketPath, httpAddr string, missing []string) {
	home, _ := os.UserHomeDir()
	for _, id := range missing {
		defaultPath := filepath.Join(home, "workspace", "projects", "engx", "services", id)
		m, err := readNexusManifest(defaultPath)
		if err != nil {
			fmt.Printf("  ○ %s: cannot read .nexus.yaml at %s\n", id, defaultPath)
			continue
		}
		if _, err := sendCommand(socketPath, daemon.CmdRegisterProject, daemon.RegisterProjectParams{
			ID: m.id, Name: m.name, Path: defaultPath,
			Language: m.language, ProjectType: m.projectType,
			ConfigJSON: m.rawYAML,
		}); err != nil {
			fmt.Printf("  ✗ %s: register failed: %v\n", id, err)
			continue
		}
		if m.runtimeProvider != "" && m.runtimeCommand != "" {
			if err := registerDefaultService(httpAddr, m, defaultPath); err != nil {
				fmt.Printf("  ! %s: service registration warning: %v\n", id, err)
			}
		}
		fmt.Printf("  ✓ %s: registered\n", id)
	}
}

// resetPlatformServices calls POST /services/:id/reset for each service.
// Best-effort — 404s are silently ignored (service not yet registered).
func resetPlatformServices(httpAddr string, serviceIDs []string) {
	for _, id := range serviceIDs {
		resp, err := http.Post(httpAddr+"/services/"+id+"/reset", "application/json", nil)
		if err != nil || resp == nil {
			continue
		}
		resp.Body.Close()
	}
}

// forEachProject sends a lifecycle command to a list of projects via the daemon socket.
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
