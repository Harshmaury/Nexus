// @nexus-project: nexus
// @nexus-path: internal/api/server.go
// Package api provides the Phase 8 HTTP/JSON API for the Nexus daemon.
//
// Architecture contract:
//   HTTP handler → controller → state.Storer
//   Handlers are thin adapters — zero business logic.
//   All decisions live in controllers, exactly as the Unix socket server does.
//
// The API server runs as a goroutine inside engxd alongside the existing
// Unix socket server. One daemon, two transports, same controllers.
//
// Fix: HTTP shutdown timeout now uses config.ShutdownTimeout constant instead
// of a hardcoded 10*time.Second literal. Changing the shutdown policy in one
// place (config/policy.go) now correctly affects all shutdown paths.
package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Harshmaury/Nexus/internal/api/handler"
	"github.com/Harshmaury/Nexus/internal/api/middleware"
	"github.com/Harshmaury/Nexus/internal/config"
	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/state"
)

// Server is the Phase 8 HTTP API server.
// It wraps net/http.Server and integrates with engxd's shutdown lifecycle.
type Server struct {
	http   *http.Server
	logger *log.Logger
}

// ServerConfig holds all dependencies the API server needs.
// Mirrors the pattern used by daemon.ServerConfig.
type ServerConfig struct {
	Addr        string                        // e.g. ":8080"
	Store       state.Storer                  // read-only queries (services, events)
	ProjectCtrl *controllers.ProjectController // all project operations
	Logger      *log.Logger
}

// NewServer wires all handlers and middleware and returns a ready Server.
// Call Run to start accepting connections.
func NewServer(cfg ServerConfig) *Server {
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}

	router := newRouter(cfg)

	return &Server{
		http: &http.Server{
			Addr:         cfg.Addr,
			Handler:      router,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
		logger: logger,
	}
}

// Run starts the HTTP server and blocks until ctx is cancelled.
// On cancellation, performs a graceful shutdown using config.ShutdownTimeout.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		s.logger.Printf("HTTP API listening on %s", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// Use config.ShutdownTimeout — single source of truth for all shutdown delays.
	// Previously hardcoded to 10*time.Second independently of the daemon's shutdown timeout.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()

	s.logger.Println("HTTP API shutting down...")
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http server shutdown: %w", err)
	}
	s.logger.Println("HTTP API stopped cleanly")
	return nil
}

// newRouter registers all routes and wraps them with middleware.
// Route pattern format: "METHOD /path/{param}" (Go 1.22+ ServeMux).
func newRouter(cfg ServerConfig) http.Handler {
	mux := http.NewServeMux()

	// ── Handlers ─────────────────────────────────────────────
	projectsH := handler.NewProjectsHandler(cfg.ProjectCtrl, cfg.Store)
	servicesH := handler.NewServicesHandler(cfg.Store)
	eventsH   := handler.NewEventsHandler(cfg.Store)

	// ── Routes ───────────────────────────────────────────────
	mux.HandleFunc("GET /health", handleHealth)

	mux.HandleFunc("GET /projects",             projectsH.List)
	mux.HandleFunc("GET /projects/{id}",        projectsH.Get)
	mux.HandleFunc("POST /projects/{id}/start", projectsH.Start)
	mux.HandleFunc("POST /projects/{id}/stop",  projectsH.Stop)
	mux.HandleFunc("POST /projects/register",   projectsH.Register)

	mux.HandleFunc("GET /services", servicesH.List)
	mux.HandleFunc("GET /events",   eventsH.List)

	// ── Middleware chain (outermost = first to run) ───────────
	var h http.Handler = mux
	h = middleware.Recovery(h, cfg.Logger)
	h = middleware.Logging(h, cfg.Logger)

	return h
}

// handleHealth is a lightweight liveness probe — no dependencies needed.
func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true,"status":"healthy"}`)) //nolint:errcheck
}
