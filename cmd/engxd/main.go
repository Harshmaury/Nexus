// @nexus-project: nexus
// @nexus-path: cmd/engxd/main.go
// engxd is the Nexus daemon — the background process that manages all services.
// It wires every component together, starts them as goroutines,
// and shuts down cleanly on SIGINT or SIGTERM.
//
// Phase 7 changes:
//   - Removed local envOrDefault, durationEnvOrDefault, expandHome, shutdownTimeout.
//     All four now come from internal/config — the single source of truth.
//   - Fixed recoveryCtrl.Run(ctx.Done()) → recoveryCtrl.Run(ctx) to match the
//     Run(ctx context.Context) error signature. The previous call passed a
//     <-chan struct{} which would not compile (and would never error-propagate).
//   - Added sync.WaitGroup so shutdown waits for all goroutines to exit before
//     the deferred store.Close() fires. Prevents in-flight DB writes being cut off.
//
// Phase 8 addition:
//   - HTTP/JSON API server added as component 8.
//     Runs alongside the Unix socket server — same context, same controllers.
//     Listen address: NEXUS_HTTP_ADDR env var (default :8080).
//
// Component startup order:
//  1. State store (SQLite)
//  2. Event bus
//  3. Providers
//  4. Controllers (project, health, recovery)
//  5. Reconciler engine
//  6. Unix socket server
//  7. HTTP API server
//  8. Result logger (reads engine + health + recovery channels)
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
	"github.com/Harshmaury/Nexus/pkg/runtime"
	dockerprovider "github.com/Harshmaury/Nexus/pkg/runtime/docker"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const daemonVersion = "0.1.0"

// ── ENTRY POINT ──────────────────────────────────────────────────────────────

func main() {
	logger := log.New(os.Stdout, "[engxd] ", log.LstdFlags)
	logger.Printf("Nexus daemon v%s starting", daemonVersion)

	if err := run(logger); err != nil {
		logger.Fatalf("fatal: %v", err)
	}

	logger.Println("daemon stopped cleanly")
}

// ── RUN ──────────────────────────────────────────────────────────────────────

func run(logger *log.Logger) error {
	// ── 1. STATE STORE ───────────────────────────────────────────────────────
	// config.EnvOrDefault and config.ExpandHome replace the former local helpers.
	dbPath := config.ExpandHome(config.EnvOrDefault("NEXUS_DB_PATH", config.DefaultDBPath))
	logger.Printf("opening state store: %s", dbPath)

	store, err := state.New(dbPath)
	if err != nil {
		return fmt.Errorf("open state store: %w", err)
	}
	// store.Close is deferred after wg.Wait() — see shutdown section below.

	// ── 2. EVENT BUS ─────────────────────────────────────────────────────────
	bus := eventbus.NewWithErrorHandler(func(topic eventbus.Topic, handlerID string, err error) {
		logger.Printf("event bus error: topic=%s handler=%s err=%v", topic, handlerID, err)
	})

	// ── 3. PROVIDERS ─────────────────────────────────────────────────────────
	providers := runtime.Providers{}

	dockerProvider, err := dockerprovider.New()
	if err != nil {
		logger.Printf("WARNING: Docker provider unavailable — docker services will not start: %v", err)
	} else {
		providers[state.ProviderDocker] = dockerProvider
		logger.Printf("registered Docker provider")
	}

	logger.Printf("providers ready: %d registered", len(providers))

	// ── 4. CONTROLLERS ───────────────────────────────────────────────────────
	projectCtrl := controllers.NewProjectController(store, bus)

	healthCtrl := controllers.NewHealthController(controllers.HealthControllerConfig{
		Store:     store,
		Bus:       bus,
		Providers: providers,
		Interval:  config.DurationEnvOrDefault("NEXUS_HEALTH_INTERVAL", config.DefaultHealthInterval),
		Timeout:   config.DurationEnvOrDefault("NEXUS_HEALTH_TIMEOUT", config.DefaultHealthTimeout),
	})

	recoveryCtrl := controllers.NewRecoveryController(store, bus)

	// ── 5. RECONCILER ────────────────────────────────────────────────────────
	engine := daemon.NewEngine(daemon.EngineConfig{
		Store:     store,
		Bus:       bus,
		Providers: providers,
		Interval:  config.DurationEnvOrDefault("NEXUS_RECONCILE_INTERVAL", config.DefaultReconcileInterval),
	})

	// ── 6. UNIX SOCKET SERVER ─────────────────────────────────────────────────
	socketPath := config.EnvOrDefault("NEXUS_SOCKET", daemon.DefaultSocketPath)
	server := daemon.NewServer(daemon.ServerConfig{
		SocketPath:  socketPath,
		Store:       store,
		Bus:         bus,
		ProjectCtrl: projectCtrl,
	})

	// ── 7. HTTP API SERVER ───────────────────────────────────────────────────
	httpAddr := config.EnvOrDefault("NEXUS_HTTP_ADDR", config.DefaultHTTPAddr)
	apiServer := api.NewServer(api.ServerConfig{
		Addr:        httpAddr,
		Store:       store,
		ProjectCtrl: projectCtrl,
		Logger:      logger,
	})

	// ── CONTEXT + SIGNAL HANDLING ─────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// ── WAITGROUP — tracks all goroutines so shutdown is clean ────────────────
	// wg.Wait() is called before store.Close(). This guarantees in-flight DB
	// writes from any goroutine complete before the connection is torn down.
	var wg sync.WaitGroup
	errCh := make(chan error, 6)

	// ── 7. START ALL GOROUTINES ───────────────────────────────────────────────

	// Reconciler
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Printf("reconciler started (interval=%s)", engine.Interval())
		if err := engine.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("reconciler: %w", err)
		}
	}()

	// Health controller
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Printf("health controller started (interval=%s)", healthCtrl.Interval())
		if err := healthCtrl.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("health controller: %w", err)
		}
	}()

	// Recovery controller — Run(ctx context.Context) error, not Run(ctx.Done()).
	// The previous call passed a <-chan struct{} which is the wrong type.
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Println("recovery controller started")
		if err := recoveryCtrl.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("recovery controller: %w", err)
		}
	}()

	// Unix socket server
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Printf("socket server started: %s", socketPath)
		if err := server.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("socket server: %w", err)
		}
	}()

	// HTTP API server
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Printf("HTTP API server started: %s", httpAddr)
		if err := apiServer.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("http api server: %w", err)
		}
	}()

	// Result logger — drains engine, health, and recovery channels.
	wg.Add(1)
	go func() {
		defer wg.Done()
		logResults(ctx, logger, engine, healthCtrl, recoveryCtrl)
	}()

	logger.Printf("✓ Nexus daemon ready — socket=%s http=%s db=%s", socketPath, httpAddr, dbPath)

	// ── WAIT FOR SHUTDOWN SIGNAL ──────────────────────────────────────────────
	select {
	case sig := <-sigCh:
		logger.Printf("received signal %s — shutting down", sig)
	case err := <-errCh:
		logger.Printf("component error — shutting down: %v", err)
	}

	cancel() // unblock all goroutines

	// Wait for all goroutines to finish in-flight work before closing the store.
	// config.ShutdownTimeout replaces the former local shutdownTimeout constant.
	// Wait for all PublishAsync goroutines to finish before components stop.
	// This ensures in-flight recovery handlers complete their store writes
	// before the WaitGroup drains and store.Close() fires.
	bus.Wait()

	logger.Printf("waiting up to %s for components to stop", config.ShutdownTimeout)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer shutdownCancel()

	select {
	case <-done:
		logger.Println("all components stopped cleanly")
	case <-shutdownCtx.Done():
		logger.Println("WARNING: shutdown timeout exceeded — forcing exit")
	}

	// Deferred inside run() so store closes after wg.Wait().
	logger.Println("closing state store")
	_ = store.Close()

	return nil
}

// ── RESULT LOGGER ────────────────────────────────────────────────────────────

// logResults drains the result channels and logs significant events.
// Keeping logging here (not in hot paths) keeps components clean.
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
				len(result.Maintained) > 0 || result.HasErrors() {
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
					result.ServiceID, result.Status, result.Message,
					result.Duration.Round(0),
				)
			}

		case decision, ok := <-recoveryCtrl.Decisions():
			if !ok {
				return
			}
			logger.Printf("recovery: service=%s action=%s reason=%s",
				decision.ServiceID, decision.Action, decision.Reason,
			)
		}
	}
}
