// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_platform.go
// Platform commands — start/stop/status for the full service fleet.
// ADR-023: reset fail counts before start.
// ADR-032: preflight registration check + --register flag.
// v2.1.0: platformStartCmd migrated to plan model (ADR-043).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/Harshmaury/Nexus/internal/plan"
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
	var autoRegister, dryRun bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start all platform services",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlatformStart(*socketPath, *httpAddr, autoRegister, dryRun)
		},
	}
	cmd.Flags().BoolVar(&autoRegister, "register", false,
		"auto-register missing platform projects before starting")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print execution plan without running")
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
			resp, err := http.Get(*httpAddr + "/services")
			if err != nil {
				return fmt.Errorf("cannot reach engxd at %s: %w", *httpAddr, err)
			}
			defer resp.Body.Close()
			var result struct {
				Data []struct {
					ID           string `json:"id"`
					Name         string `json:"name"`
					DesiredState string `json:"desired_state"`
					ActualState  string `json:"actual_state"`
					FailCount    int    `json:"fail_count"`
				} `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			total, running := 0, 0
			for _, svc := range result.Data {
				total++
				if svc.ActualState == "running" {
					running++
				}
			}
			fmt.Printf("Platform: %d/%d services running\n", running, total)
			for _, svc := range result.Data {
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


// runPlatformStart builds and executes (or prints) the platform start plan.
func runPlatformStart(socketPath, httpAddr string, autoRegister, dryRun bool) error {
	cfg := plan.RunConfig{NexusAddr: httpAddr}
	p := buildPlatformStartPlan(socketPath, httpAddr, autoRegister)
	if dryRun {
		plan.Print(p, os.Stdout)
		return nil
	}
	return plan.Run(context.Background(), p, os.Stdout, cfg)
}

// buildPlatformStartPlan constructs the platform start plan.
func buildPlatformStartPlan(socketPath, httpAddr string, autoRegister bool) *plan.Plan {
	return plan.Build("platform:start", []*plan.Step{
		{Label: "Checking registration", Kind: plan.KindValidate, Run: stepCheckRegistration(socketPath, httpAddr, autoRegister)},
		{Label: "Resetting services",    Kind: plan.KindExecute,  Run: stepResetServices(httpAddr)},
		{Label: "Starting services",     Kind: plan.KindExecute,  Run: stepStartAllProjects(socketPath)},
		{Label: "Waiting for platform",  Kind: plan.KindWait,     Run: stepWaitPlatform(httpAddr)},
	})
}

func stepCheckRegistration(socketPath, httpAddr string, autoRegister bool) plan.StepFunc {
	return func(_ context.Context) plan.StepResult {
		missing := checkPlatformRegistered(httpAddr, platformProjects)
		if len(missing) == 0 {
			return plan.StepResult{OK: true, Detail: "all projects registered"}
		}
		if !autoRegister {
			return plan.StepResult{OK: false, Err: &plan.UserError{
				What: fmt.Sprintf("%d project(s) not registered", len(missing)),
				Why:  fmt.Sprintf("missing: %v", missing),
				NextStep: "engx platform start --register",
			}}
		}
		autoRegisterPlatform(socketPath, httpAddr, missing)
		return plan.StepResult{OK: true, Detail: fmt.Sprintf("registered %d project(s)", len(missing))}
	}
}

func stepResetServices(httpAddr string) plan.StepFunc {
	return func(_ context.Context) plan.StepResult {
		resetPlatformServices(httpAddr, platformServiceIDs)
		return plan.StepResult{OK: true, Detail: "fail counts reset"}
	}
}

func stepStartAllProjects(socketPath string) plan.StepFunc {
	return func(_ context.Context) plan.StepResult {
		started := 0
		for _, proj := range platformProjects {
			resp, err := sendCommand(socketPath, daemon.CmdProjectStart,
				daemon.ProjectStartParams{ProjectID: proj})
			if err != nil {
				continue
			}
			var r map[string]any
			_ = json.Unmarshal(resp.Data, &r)
			if q, _ := r["queued"].(float64); int(q) > 0 {
				started++
			}
		}
		return plan.StepResult{OK: true, Detail: fmt.Sprintf("queued %d/%d projects", started, len(platformProjects))}
	}
}

func stepWaitPlatform(httpAddr string) plan.StepFunc {
	return func(_ context.Context) plan.StepResult {
		running, total := 0, 0
		for _, proj := range platformProjects {
			r, t, err := projectServiceStates(httpAddr, proj)
			if err != nil {
				continue
			}
			running += r
			total += t
		}
		if running == total && total > 0 {
			return plan.StepResult{OK: true,
				Message: fmt.Sprintf("%d/%d ✓", running, total),
				Detail:  fmt.Sprintf("%d/%d services running", running, total)}
		}
		return plan.StepResult{OK: true, Skip: true,
			Message: fmt.Sprintf("%d/%d running (check: engx ps)", running, total)}
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
