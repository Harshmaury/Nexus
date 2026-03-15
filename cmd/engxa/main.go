// @nexus-project: nexus
// @nexus-path: cmd/engxa/main.go
// engxa is the Nexus remote agent — runs on non-central machines.
// It connects to the central engxd, syncs desired service states,
// and reconciles them locally using the machine's runtime providers.
//
// Usage:
//   engxa --server http://192.168.1.10:8080 --token <secret>
//   engxa --server http://192.168.1.10:8080 --token <secret> --id my-server-1
//
// Environment variables (alternative to flags):
//   NEXUS_SERVER   central engxd HTTP address
//   NEXUS_TOKEN    shared secret
//   NEXUS_AGENT_ID stable machine identifier (default: OS hostname)
//   NEXUS_ADDR     IP:port this agent is reachable on (informational)
//
// Build:
//   go build -o ~/bin/engxa ./cmd/engxa/
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Harshmaury/Nexus/internal/agent"
	"github.com/Harshmaury/Nexus/internal/config"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/pkg/runtime"
	dockerprovider  "github.com/Harshmaury/Nexus/pkg/runtime/docker"
	k8sprovider     "github.com/Harshmaury/Nexus/pkg/runtime/k8s"
	processprovider "github.com/Harshmaury/Nexus/pkg/runtime/process"
	"github.com/spf13/cobra"
)

const agentVersion = "0.1.0"

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var (
		serverURL string
		token     string
		agentID   string
		address   string
	)

	cmd := &cobra.Command{
		Use:     "engxa",
		Short:   "Nexus Remote Agent — syncs service state with a central engxd",
		Version: agentVersion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(serverURL, token, agentID, address)
		},
	}

	cmd.Flags().StringVar(&serverURL, "server", config.EnvOrDefault("NEXUS_SERVER", ""),
		"central engxd HTTP address (e.g. http://192.168.1.10:8080)")
	cmd.Flags().StringVar(&token, "token", config.EnvOrDefault("NEXUS_TOKEN", ""),
		"shared secret token (must match agents table on server)")
	cmd.Flags().StringVar(&agentID, "id", config.EnvOrDefault("NEXUS_AGENT_ID", ""),
		"stable agent identifier (default: OS hostname)")
	cmd.Flags().StringVar(&address, "addr", config.EnvOrDefault("NEXUS_ADDR", ""),
		"IP:port this agent is reachable on (informational)")

	return cmd
}

func run(serverURL, token, agentID, address string) error {
	logger := log.New(os.Stdout, "[engxa] ", log.LstdFlags)
	logger.Printf("Nexus Agent v%s starting", agentVersion)

	if serverURL == "" {
		return fmt.Errorf("--server is required (or set NEXUS_SERVER)")
	}
	if token == "" {
		return fmt.Errorf("--token is required (or set NEXUS_TOKEN)")
	}

	// ── Providers ─────────────────────────────────────────────────────────
	providers := runtime.Providers{}

	if dp, err := dockerprovider.New(); err != nil {
		logger.Printf("WARNING: Docker provider unavailable: %v", err)
	} else {
		providers[state.ProviderDocker] = dp
		logger.Printf("registered Docker provider")
	}

	if pp, err := processprovider.New(); err != nil {
		logger.Printf("WARNING: Process provider unavailable: %v", err)
	} else {
		providers[state.ProviderProcess] = pp
		logger.Printf("registered Process provider")
	}

	if kp, err := k8sprovider.New(); err != nil {
		logger.Printf("WARNING: K8s provider unavailable: %v", err)
	} else {
		providers[state.ProviderK8s] = kp
		logger.Printf("registered K8s provider")
	}

	logger.Printf("providers ready: %d registered", len(providers))

	// ── Agent ─────────────────────────────────────────────────────────────
	a, err := agent.New(agent.Config{
		AgentID:   agentID,
		ServerURL: serverURL,
		Token:     token,
		Address:   address,
		Providers: providers,
	})
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	// ── Signal handling ───────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		logger.Printf("received %s — shutting down", sig)
		cancel()
	}()

	logger.Printf("connecting to %s", serverURL)
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		return fmt.Errorf("agent: %w", err)
	}

	logger.Println("agent stopped cleanly")
	return nil
}
