// @nexus-project: nexus
// @nexus-path: cmd/engxd/main.go
// engxd is the Nexus daemon — the background process that manages all services.
// It wires every component together, starts them as goroutines,
// and shuts down cleanly on SIGINT or SIGTERM.
//
// Component startup order:
//  1. State store (SQLite)
//  2. Event bus
//  3. Controllers (project, health, recovery)
//  4. Reconciler engine
//  5. Unix socket server
//  6. Result logger (reads engine + health channels)
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/pkg/runtime"
	dockerprovider "github.com/Harshmaury/Nexus/pkg/runtime/docker"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	daemonVersion    = "0.1.0"
	defaultDBPath    = "~/.nexus/nexus.db"
	shutdownTimeout  = 10 * time.Second
)

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
	dbPath := expandHome(envOrDefault("NEXUS_DB_PATH", defaultDBPath))
	logger.Printf("opening state store: %s", dbPath)

	store, err := state.New(dbPath)
	if err != nil {
		return fmt.Errorf("open state store: %w", err)
	}
	defer func() {
		logger.Println("closing state store")
		_ = store.Close()
	}()

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
		Interval:  durationEnvOrDefault("NEXUS_HEALTH_INTERVAL", 10*time.Second),
		Timeout:   durationEnvOrDefault("NEXUS_HEALTH_TIMEOUT", 5*time.Second),
	})

	recoveryCtrl := controllers.NewRecoveryController(store, bus)

	// ── 5. RECONCILER ────────────────────────────────────────────────────────
	engine := daemon.NewEngine(daemon.EngineConfig{
		Store:     store,
		Bus:       bus,
		Providers: providers,
		Interval:  durationEnvOrDefault("NEXUS_RECONCILE_INTERVAL", 5*time.Second),
	})

	// ── 6. UNIX SOCKET SERVER ────────────────────────────────────────────────
	socketPath := envOrDefault("NEXUS_SOCKET", daemon.DefaultSocketPath)
	server := daemon.NewServer(daemon.ServerConfig{
		SocketPath:  socketPath,
		Store:       store,
		Bus:         bus,
		ProjectCtrl: projectCtrl,
	})

	// ── CONTEXT + SIGNAL HANDLING ────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// ── START ALL GOROUTINES ─────────────────────────────────────────────────
	errCh := make(chan error, 5)
	done := make(chan struct{})

	// Reconciler
	go func() {
		logger.Printf("reconciler started (interval=%s)", engine.Interval())
		if err := engine.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("reconciler: %w", err)
		}
	}()

	// Health controller
	go func() {
		logger.Printf("health controller started (interval=%s)", healthCtrl.Interval())
		if err := healthCtrl.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("health controller: %w", err)
		}
	}()

	// Recovery controller
	go func() {
		logger.Println("recovery controller started")
		recoveryCtrl.Run(ctx.Done())
	}()

	// Unix socket server
	go func() {
		logger.Printf("socket server started: %s", socketPath)
		if err := server.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("socket server: %w", err)
		}
	}()

	// Result logger — reads reconciler and health channels and logs them.
	go logResults(ctx, logger, engine, healthCtrl, recoveryCtrl)

	logger.Printf("✓ Nexus daemon ready — socket=%s db=%s", socketPath, dbPath)

	// ── WAIT FOR SHUTDOWN ────────────────────────────────────────────────────
	go func() {
		select {
		case sig := <-sigCh:
			logger.Printf("received signal %s — shutting down", sig)
		case err := <-errCh:
			logger.Printf("component error — shutting down: %v", err)
		}
		cancel()
		close(done)
	}()

	<-done

	// Give components time to finish in-flight work.
	logger.Printf("waiting up to %s for components to stop", shutdownTimeout)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	<-shutdownCtx.Done()
	return nil
}

// ── RESULT LOGGER ────────────────────────────────────────────────────────────

// logResults subscribes to result channels and logs significant events.
// This keeps logging out of the hot paths of each component.
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
			// Only log cycles where something happened.
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
			// Only log unhealthy results — skip noise from healthy services.
			if !result.IsHealthy() {
				logger.Printf("health: service=%s status=%s message=%s duration=%s",
					result.ServiceID, result.Status, result.Message,
					result.Duration.Round(time.Millisecond),
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

// ── HELPERS ──────────────────────────────────────────────────────────────────

func envOrDefault(key string, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func durationEnvOrDefault(key string, fallback time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		return fallback
	}
	return d
}

func expandHome(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home + "/" + path[2:]
	}
	return path
}
