// @nexus-project: nexus
// @nexus-path: cmd/engx/main.go
// engx — CLI for the Nexus Local Developer Control Plane.
// Wave 5 refactor: main.go contains only wiring.
// Each command group lives in its own file:
//   cmd_platform.go   — platform start/stop/status (ADR-032)
//   cmd_project.go    — project/services/events/agents (ADR-033 deregister)
//   cmd_register.go   — register + nexusManifest
//   cmd_forge.go      — build/check/trace/run
//   cmd_onboard.go    — init + language detection
//   cmd_observe.go    — watch + drop suite
//   cmd_ops.go        — logs + version
//   cmd_doctor.go     — doctor diagnosis
//   cmd_shared.go     — getJSON, sendCommand, formatUptime, truncate
//   cmd_automation.go — workflow/trigger/guard/on/exec/ci/stream
//   cmd_upgrade.go    — upgrade (ADR-028)
//   cmd_install.go    — platform install/uninstall (ADR-026)
//   cmd_follow.go     — logs --follow
//   cmd_ci.go         — ci machine-readable commands
//   cmd_doctor_fs.go  — filesystem/environment doctor checks
package main

import (
	"fmt"
	"os"

	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/spf13/cobra"
)

const cliVersion = "1.5.0"

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
		// Project lifecycle
		projectCmd(&socketPath),
		registerCmd(&socketPath, &httpAddr),
		servicesCmd(&socketPath, &httpAddr),
		eventsCmd(&socketPath),
		agentsCmd(&httpAddr),
		// Platform
		platformCmd(&socketPath, &httpAddr),
		// Observe
		dropCmd(&socketPath),
		watchCmd(&socketPath),
		// Forge integration
		buildCmd(&httpAddr),
		checkCmd(&httpAddr),
		runCmd(&socketPath, &httpAddr),
		traceCmd(),
		// Onboarding
		initCmd(&socketPath, &httpAddr),
		// Ops
		logsCmd(),
		logsFollowCmd(),
		versionCmd(),
		// Diagnosis
		doctorCmd(&httpAddr),
		statusCmd(&httpAddr),
		sentinelCmd(&httpAddr),
		// Automation
		workflowCmd(&httpAddr),
		triggerCmd(&httpAddr),
		guardCmd(&httpAddr),
		onCmd(&httpAddr),
		execCmd(&httpAddr),
		ciCmd(&httpAddr),
		eventsStreamCmd(&httpAddr),
		// Maintenance
		upgradeCmd(&httpAddr),
	)

	return root
}
