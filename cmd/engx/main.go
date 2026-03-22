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

// cliVersion is overridden at build time by goreleaser via -ldflags "-X main.cliVersion=...".
var cliVersion = "dev"

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
		// ── Visible: user-facing commands ──────────────────────────────────
		runCmd(&socketPath, &httpAddr),
		projectStatusUXCmd(&httpAddr),
		statusCmd(&httpAddr),
		registerCmd(&socketPath, &httpAddr),
		initCmd(&socketPath, &httpAddr),
		buildCmd(&httpAddr),
		checkCmd(&httpAddr),
		traceCmd(),
		logsCmd(),
		doctorCmd(&httpAddr),
		platformCmd(&socketPath, &httpAddr),
		projectCmd(&socketPath),
		upgradeCmd(&httpAddr),
		versionCmd(),
		loginCmd(),
		logoutCmd(),
		whoamiCmd(),
		// ── Developer insight commands ──────────────────────────────────────
		whyCmd(&httpAddr),
		activityCmd(&httpAddr),
		psDetailCmd(&httpAddr),
		historyCmd(),
	)

	// ── Hidden: advanced/internal commands (ADR-040) ─────────────────────
	for _, advanced := range []*cobra.Command{
		eventsCmd(&socketPath),
		agentsCmd(&httpAddr),
		dropCmd(&socketPath),
		watchCmd(&socketPath),
		servicesCmd(&socketPath, &httpAddr),
		sentinelCmd(&httpAddr),
		workflowCmd(&httpAddr),
		triggerCmd(&httpAddr),
		guardCmd(&httpAddr),
		onCmd(&httpAddr),
		execCmd(&httpAddr),
		ciCmd(&httpAddr),
		eventsStreamCmd(&httpAddr),
		logsFollowCmd(),
	} {
		advanced.Hidden = true
		root.AddCommand(advanced)
	}

	// Progressive disclosure — hide internal commands from default help (ADR-040).
	for _, name := range []string{
		"events", "agents", "drop", "watch",
		"sentinel", "workflow", "trigger", "guard", "on", "exec",
		"ci", "stream",
	} {
		if cmd, _, err := root.Find([]string{name}); err == nil && cmd != nil {
			cmd.Hidden = true
		}
	}

	return root
}
