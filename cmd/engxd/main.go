// @nexus-project: nexus
// @nexus-path: cmd/engxd/main.go
// ADR-008 addition:
//   Service token file loaded at startup (step 1).
//   Passed into api.ServerConfig.ServiceTokens.
//   If ~/.nexus/service-tokens is absent, auth is skipped with a WARNING.
//
// ADR-002 addition:
//   Nexus now watches the workspace root in addition to the drop folder.
//   The workspace watcher publishes workspace event topics through the event bus
//   so Atlas and Forge can subscribe without running independent watchers.
//
//   WatchTarget{Dir: workspaceRoot, Mode: WatchModeWorkspace} is added
//   to the multi-target watcher alongside the existing drop folder target.
//
//   NEXUS_WORKSPACE env var controls the workspace root (default ~/workspace).
//   NEXUS_DROP_DIR  env var controls the drop folder  (default ~/nexus-drop).
//
// Component startup order:
//  1. Metrics + service tokens (ADR-008)
//  2. State store (SQLite)
//  3. Event bus
//  4. Providers (Docker, Process, K8s)
//  5. Controllers (project, health, recovery)
//  6. Reconciler engine
//  7. Unix socket server
//  8. HTTP API server
//  9. Watcher (drop folder + workspace — ADR-002)
// 10. Result logger
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/Harshmaury/Nexus/internal/api"
	"github.com/Harshmaury/Nexus/internal/config"
	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/internal/telemetry"
	"github.com/Harshmaury/Nexus/internal/watcher"
	"github.com/Harshmaury/Nexus/pkg/runtime"
	dockerprovider  "github.com/Harshmaury/Nexus/pkg/runtime/docker"
	k8sprovider     "github.com/Harshmaury/Nexus/pkg/runtime/k8s"
	processprovider "github.com/Harshmaury/Nexus/pkg/runtime/process"
)

const daemonVersion = "0.1.0"

func main() {
	logger := log.New(os.Stdout, "[engxd] ", log.LstdFlags)
	logger.Printf("Nexus daemon v%s starting", daemonVersion)
	if err := run(logger); err != nil {
		logger.Fatalf("fatal: %v", err)
	}
	logger.Println("daemon stopped cleanly")
}

func run(logger *log.Logger) error {
	// ── 1. METRICS + SERVICE TOKENS ────────────────────────────────────────
	metrics := telemetry.New()

	// ADR-008: load inter-service auth tokens.
	// If the file does not exist, serviceTokens is empty and auth is skipped.
	serviceTokens, err := config.LoadServiceTokens(config.ServiceTokensPath)
	if err != nil {
		logger.Printf("WARNING: cannot load service-tokens: %v — running unauthenticated", err)
		serviceTokens = map[string]string{}
	} else if len(serviceTokens) == 0 {
		logger.Println("WARNING: ~/.nexus/service-tokens not found — inter-service auth disabled")
	}

	// ── 2. STATE STORE ───────────────────────────────────────────────────────
	dbPath := config.ExpandHome(config.EnvOrDefault("NEXUS_DB_PATH", config.DefaultDBPath))
	logger.Printf("opening state store: %s", dbPath)
	store, err := state.New(dbPath)
	if err != nil {
		return fmt.Errorf("open state store: %w", err)
	}

	// ── 3. EVENT BUS ─────────────────────────────────────────────────────────
	bus := eventbus.NewWithErrorHandler(func(topic eventbus.Topic, handlerID string, err error) {
		logger.Printf("event bus error: topic=%s handler=%s err=%v", topic, handlerID, err)
	})

	// ── 4. PROVIDERS ─────────────────────────────────────────────────────────
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

	// ── 5. CONTROLLERS ───────────────────────────────────────────────────────
	projectCtrl  := controllers.NewProjectController(store, bus)
	healthCtrl   := controllers.NewHealthController(controllers.HealthControllerConfig{
		Store:     store,
		Bus:       bus,
		Providers: providers,
		Interval:  config.DurationEnvOrDefault("NEXUS_HEALTH_INTERVAL", config.DefaultHealthInterval),
		Timeout:   config.DurationEnvOrDefault("NEXUS_HEALTH_TIMEOUT", config.DefaultHealthTimeout),
	})
	recoveryCtrl := controllers.NewRecoveryController(store, bus)

	// ── 6. RECONCILER ────────────────────────────────────────────────────────
	engine := daemon.NewEngine(daemon.EngineConfig{
		Store:     store,
		Bus:       bus,
		Providers: providers,
		Metrics:   metrics,
		Interval:  config.DurationEnvOrDefault("NEXUS_RECONCILE_INTERVAL", config.DefaultReconcileInterval),
	})

	// ── 7. UNIX SOCKET SERVER ─────────────────────────────────────────────────
	socketPath := config.EnvOrDefault("NEXUS_SOCKET", daemon.DefaultSocketPath)
	server := daemon.NewServer(daemon.ServerConfig{
		SocketPath:  socketPath,
		Store:       store,
		Bus:         bus,
		ProjectCtrl: projectCtrl,
	})

	// ── 8. HTTP API SERVER ───────────────────────────────────────────────────
	httpAddr := config.EnvOrDefault("NEXUS_HTTP_ADDR", config.DefaultHTTPAddr)
	apiServer := api.NewServer(api.ServerConfig{
		Addr:          httpAddr,
		Store:         store,
		ProjectCtrl:   projectCtrl,
		Metrics:       metrics,
		Logger:        logger,
		ServiceTokens: serviceTokens, // ADR-008
	})

	// ── 9. WATCHER (drop folder + workspace — ADR-002) ────────────────────────
	dropDir       := config.ExpandHome(config.EnvOrDefault("NEXUS_DROP_DIR", "~/nexus-drop"))
	workspaceRoot := config.ExpandHome(config.EnvOrDefault("NEXUS_WORKSPACE", "~/workspace"))

	w := watcher.NewMulti(
		[]watcher.WatchTarget{
			{Dir: dropDir,       Mode: watcher.WatchModeDropFolder},
			{Dir: workspaceRoot, Mode: watcher.WatchModeWorkspace},
		},
		bus,
		store,
	)

	// ── CONTEXT + SIGNALS ─────────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup
	errCh := make(chan error, 7)

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Printf("reconciler started (interval=%s)", engine.Interval())
		if err := engine.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("reconciler: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Printf("health controller started (interval=%s)", healthCtrl.Interval())
		if err := healthCtrl.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("health controller: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Println("recovery controller started")
		if err := recoveryCtrl.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("recovery controller: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Printf("socket server started: %s", socketPath)
		if err := server.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("socket server: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Printf("HTTP API started: %s", httpAddr)
		if err := apiServer.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("http api: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Printf("watcher started — drop=%s workspace=%s", dropDir, workspaceRoot)
		if err := w.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("watcher: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		logResults(ctx, logger, engine, healthCtrl, recoveryCtrl)
	}()

	logger.Printf("✓ Nexus ready — socket=%s http=%s metrics=%s/metrics drop=%s workspace=%s",
		socketPath, httpAddr, httpAddr, dropDir, workspaceRoot)

	// ── WAIT FOR SHUTDOWN ─────────────────────────────────────────────────────
	select {
	case sig := <-sigCh:
		logger.Printf("received %s — shutting down", sig)
	case err := <-errCh:
		logger.Printf("component error: %v — shutting down", err)
	}

	cancel()
	bus.Wait()

	logger.Printf("waiting up to %s for components to stop", config.ShutdownTimeout)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer shutdownCancel()

	select {
	case <-done:
		logger.Println("all components stopped cleanly")
	case <-shutdownCtx.Done():
		logger.Println("WARNING: shutdown timeout exceeded")
	}

	logger.Println("closing state store")
	_ = store.Close()
	return nil
}

func logResults(
	ctx context.Context,
	logger *log.Logger,
	engine *daemon.Engine,
	healthCtrl *controllers.HealthController,
	recoveryCtrl *controllers.RecoveryController,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case result, ok := <-engine.Results():
			if !ok {
				return
			}
			if len(result.Started) > 0 || len(result.Stopped) > 0 ||
				len(result.Maintained) > 0 || len(result.Deferred) > 0 || result.HasErrors() {
				logger.Printf("reconcile: %s", result.Summary())
			}
			for _, e := range result.Errors {
				logger.Printf("reconcile error: %s", e.Error())
			}
		case result, ok := <-healthCtrl.Results():
			if !ok {
				return
			}
			if !result.IsHealthy() {
				logger.Printf("health: service=%s status=%s message=%s duration=%s",
					result.ServiceID, result.Status, result.Message, result.Duration.Round(0))
			}
		case decision, ok := <-recoveryCtrl.Decisions():
			if !ok {
				return
			}
			logger.Printf("recovery: service=%s action=%s reason=%s",
				decision.ServiceID, decision.Action, decision.Reason)
		}
	}
}
