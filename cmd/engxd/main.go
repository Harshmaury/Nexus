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
	"path/filepath"
	"sync"
	"syscall"

	"github.com/Harshmaury/Nexus/internal/api"
	"github.com/Harshmaury/Nexus/internal/config"
	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/mode"
	"github.com/Harshmaury/Nexus/internal/sse"
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

	// ── 1b. BOOTSTRAP ~/.nexus/ DIRECTORY STRUCTURE ──────────────────────────
	if err := bootstrapNexusHome(logger); err != nil {
		return fmt.Errorf("bootstrap ~/.nexus/: %w — fix directory permissions and retry", err)
	}

	// ── 1c. TOKEN FILE PERMISSION CHECK (audit fix) ───────────────────────────
	checkTokenFilePerms(config.ExpandHome(config.ServiceTokensPath), logger)

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
	sseBroker := sse.NewBroker()

	// ── 8a. RUNTIME MODE EVALUATOR (ADR-044) ─────────────────────────────────
	modeEval := mode.NewEvaluator(mode.EvaluatorConfig{
		GateAddr:        config.EnvOrDefault("GATE_HTTP_ADDR", "http://127.0.0.1:8088"),
		GuardianAddr:    config.EnvOrDefault("GUARDIAN_HTTP_ADDR", "http://127.0.0.1:8085"),
		SentinelAddr:    config.EnvOrDefault("SENTINEL_HTTP_ADDR", "http://127.0.0.1:8087"),
		HasServiceToken: len(serviceTokens) > 0,
		HasSSEBroker:    true,
	})
	logger.Printf(modeEval.ModeLogLine())

	// ── 8. HTTP API SERVER ───────────────────────────────────────────────────
	// ── 8. HTTP API SERVER ───────────────────────────────────────────────────
	apiServer := api.NewServer(api.ServerConfig{
		Addr:          httpAddr,
		Store:         store,
		ProjectCtrl:   projectCtrl,
		Metrics:       metrics,
		Logger:        logger,
		ServiceTokens: serviceTokens, // ADR-008
		SSEBroker:     sseBroker,     // Phase 16: ADR-015
		DaemonVersion: daemonVersion, // Phase 22: binary version cross-check
		ModeEvaluator: modeEval,      // Phase 23: ADR-044
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

	logger.Printf("reconciler started (interval=%s)", engine.Interval())
	safeRun(ctx, "reconciler", &wg, errCh, engine.Run)

	logger.Printf("health controller started (interval=%s)", healthCtrl.Interval())
	safeRun(ctx, "health controller", &wg, errCh, healthCtrl.Run)

	logger.Println("recovery controller started")
	safeRun(ctx, "recovery controller", &wg, errCh, recoveryCtrl.Run)

	logger.Printf("socket server started: %s", socketPath)
	safeRun(ctx, "socket server", &wg, errCh, server.Run)

	logger.Printf("HTTP API started: %s", httpAddr)
	safeRun(ctx, "http api", &wg, errCh, apiServer.Run)

	logger.Printf("watcher started — drop=%s workspace=%s", dropDir, workspaceRoot)
	safeRun(ctx, "watcher", &wg, errCh, w.Run)

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

// bootstrapNexusHome ensures the ~/.nexus/ directory structure exists.
func bootstrapNexusHome(logger *log.Logger) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}
	dirs := []string{
		filepath.Join(home, ".nexus"),
		filepath.Join(home, ".nexus", "logs"),
		filepath.Join(home, ".nexus", "backups"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	return nil
}

// checkTokenFilePerms warns if service-tokens is world-readable.
func checkTokenFilePerms(tokenPath string, logger *log.Logger) {
	info, err := os.Stat(tokenPath)
	if err != nil {
		return
	}
	mode := info.Mode().Perm()
	if mode&0o044 != 0 {
		logger.Printf("WARNING: token file has unsafe permissions (%04o): %s", mode, tokenPath)
		logger.Printf("  Fix: chmod 600 %s", tokenPath)
	}
}

// safeRun launches fn(ctx) in a goroutine tracked by wg.
// Catches panics and converts them to clean shutdown via errCh.
func safeRun(
	ctx context.Context,
	name string,
	wg *sync.WaitGroup,
	errCh chan<- error,
	fn func(context.Context) error,
) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("%s: panic: %v", name, r)
			}
		}()
		if err := fn(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("%s: %w", name, err)
		}
	}()
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
