// @nexus-project: nexus
// @nexus-path: cmd/engxd/main.go
// engxd is the Nexus daemon — the background process that manages all services.
// It wires every component together, starts them as goroutines,
// and shuts down cleanly on SIGINT or SIGTERM.
//
// Changes from previous version:
//   - sync.WaitGroup tracks goroutines — shutdown no longer always burns 10s
//   - log.Logger replaced with log/slog + JSONHandler (structured, queryable)
//   - RecoveryController.Run now called with context.Context (not ctx.Done())
//   - All config defaults imported from internal/config (no local constants)
//   - expandHome / envOrDefault / durationEnvOrDefault moved to internal/config
//
// Component startup order:
//  1. State store (SQLite)
//  2. Event bus
//  3. Controllers (project, health, recovery)
//  4. Reconciler engine
//  5. Unix socket server
//  6. Result logger
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/Harshmaury/Nexus/internal/config"
	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/daemon"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/pkg/runtime"
)

const daemonVersion = "0.1.0"

// ── ENTRY POINT ──────────────────────────────────────────────────────────────

func main() {
	logger := setupLogger()

	logger.Info("Nexus daemon starting", "version", daemonVersion)

	if err := run(logger); err != nil {
		logger.Error("daemon exited with error", "err", err)
		os.Exit(1)
	}

	logger.Info("daemon stopped cleanly")
}

// ── LOGGER SETUP ─────────────────────────────────────────────────────────────

// setupLogger builds a structured JSON logger respecting LOG_LEVEL env var.
// Output is machine-readable and ships directly to Grafana Loki without parsing.
func setupLogger() *slog.Logger {
	level := slog.LevelInfo
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			slog.Warn("invalid LOG_LEVEL, defaulting to info", "value", v)
		}
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}

// ── RUN ──────────────────────────────────────────────────────────────────────

func run(logger *slog.Logger) error {
	// ── 1. STATE STORE ───────────────────────────────────────────────────────
	dbPath := config.ExpandHome(config.EnvOrDefault("NEXUS_DB_PATH", config.DefaultDBPath))
	logger.Info("opening state store", "path", dbPath)

	store, err := state.New(dbPath)
	if err != nil {
		return fmt.Errorf("open state store: %w", err)
	}
	defer func() {
		logger.Info("closing state store")
		_ = store.Close()
	}()

	// ── 2. EVENT BUS ─────────────────────────────────────────────────────────
	bus := eventbus.NewWithErrorHandler(func(topic eventbus.Topic, handlerID string, err error) {
		logger.Warn("event bus handler error",
			"topic", topic,
			"handler_id", handlerID,
			"err", err,
		)
	})

	// ── 3. PROVIDERS ─────────────────────────────────────────────────────────
	providers := runtime.Providers{}
	logger.Info("providers registered", "count", len(providers))

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
		Logger:    logger,
	})

	// ── 6. UNIX SOCKET SERVER ────────────────────────────────────────────────
	socketPath := config.EnvOrDefault("NEXUS_SOCKET", daemon.DefaultSocketPath)
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

	// ── START GOROUTINES ─────────────────────────────────────────────────────
	// WaitGroup tracks all goroutines so shutdown waits for actual completion
	// instead of always burning the full shutdown timeout.
	var wg sync.WaitGroup
	errCh := make(chan error, 5)

	startComponent := func(name string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil && ctx.Err() == nil {
				errCh <- fmt.Errorf("%s: %w", name, err)
			}
		}()
	}

	startComponent("reconciler", func() error {
		logger.Info("reconciler started", "interval", engine.Interval().String())
		return engine.Run(ctx)
	})

	startComponent("health-controller", func() error {
		logger.Info("health controller started", "interval", healthCtrl.Interval().String())
		return healthCtrl.Run(ctx)
	})

	startComponent("recovery-controller", func() error {
		logger.Info("recovery controller started")
		return recoveryCtrl.Run(ctx) // context.Context — fixed from ctx.Done()
	})

	startComponent("socket-server", func() error {
		logger.Info("socket server started", "socket", socketPath)
		return server.Run(ctx)
	})

	// Result logger reads all result channels — no WaitGroup needed (ctx-driven).
	go logResults(ctx, logger, engine, healthCtrl, recoveryCtrl)

	logger.Info("Nexus daemon ready",
		"socket", socketPath,
		"db", dbPath,
		"version", daemonVersion,
	)

	// ── WAIT FOR SHUTDOWN TRIGGER ────────────────────────────────────────────
	select {
	case sig := <-sigCh:
		logger.Info("shutdown signal received", "signal", sig.String())
	case err := <-errCh:
		logger.Error("component error — initiating shutdown", "err", err)
	}

	cancel() // signal all components to stop

	// ── WAIT FOR COMPONENTS TO FINISH ────────────────────────────────────────
	// Use a channel to detect actual completion vs forced timeout.
	// Previously: always waited the full 10s. Now: exits as soon as all
	// goroutines return, with 10s as a hard safety ceiling.
	stopped := make(chan struct{})
	go func() {
		wg.Wait()
		close(stopped)
	}()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer shutdownCancel()

	select {
	case <-stopped:
		logger.Info("all components stopped cleanly")
	case <-shutdownCtx.Done():
		logger.Warn("shutdown timeout exceeded — forcing exit",
			"timeout", config.ShutdownTimeout.String(),
		)
	}

	return nil
}

// ── RESULT LOGGER ────────────────────────────────────────────────────────────

// logResults subscribes to result channels and logs significant events.
// Keeps structured logging out of the hot paths of each component.
func logResults(
	ctx context.Context,
	logger *slog.Logger,
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
				logger.Info("reconcile cycle",
					"cycle_id", result.CycleID,
					"started", len(result.Started),
					"stopped", len(result.Stopped),
					"maintained", len(result.Maintained),
					"skipped", len(result.Skipped),
					"errors", len(result.Errors),
					"duration_ms", result.Duration.Milliseconds(),
				)
			}
			for _, e := range result.Errors {
				logger.Error("reconcile error",
					"service_id", e.ServiceID,
					"action", e.Action,
					"err", e.Err,
				)
			}

		case result, ok := <-healthCtrl.Results():
			if !ok {
				return
			}
			if !result.IsHealthy() {
				logger.Warn("health check failed",
					"service_id", result.ServiceID,
					"status", result.Status,
					"message", result.Message,
					"duration_ms", result.Duration.Milliseconds(),
				)
			}

		case decision, ok := <-recoveryCtrl.Decisions():
			if !ok {
				return
			}
			logger.Info("recovery decision",
				"service_id", decision.ServiceID,
				"action", decision.Action,
				"reason", decision.Reason,
				"back_off_delay", decision.BackOffDelay.String(),
			)
		}
	}
}
