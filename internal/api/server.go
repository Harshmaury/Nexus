// @nexus-project: nexus
// @nexus-path: internal/api/server.go
// Phase 14 addition:
//   All /agents routes registered.
//   AgentsHandler injected via ServerConfig.Store (no new config field needed).
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Harshmaury/Nexus/internal/api/handler"
	"github.com/Harshmaury/Nexus/internal/api/middleware"
	"github.com/Harshmaury/Nexus/internal/config"
	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/internal/telemetry"
)

type Server struct {
	http   *http.Server
	logger *log.Logger
}

type ServerConfig struct {
	Addr        string
	Store       state.Storer
	ProjectCtrl *controllers.ProjectController
	Metrics     *telemetry.Metrics
	Logger      *log.Logger
}

func NewServer(cfg ServerConfig) *Server {
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &Server{
		http: &http.Server{
			Addr:         cfg.Addr,
			Handler:      newRouter(cfg),
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
		logger: logger,
	}
}

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()

	s.logger.Println("HTTP API shutting down...")
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	s.logger.Println("HTTP API stopped cleanly")
	return nil
}

func newRouter(cfg ServerConfig) http.Handler {
	mux := http.NewServeMux()

	projectsH := handler.NewProjectsHandler(cfg.ProjectCtrl, cfg.Store)
	servicesH := handler.NewServicesHandler(cfg.Store)
	eventsH   := handler.NewEventsHandler(cfg.Store)
	agentsH   := handler.NewAgentsHandler(cfg.Store)

	// ── Core routes ──────────────────────────────────────────────────────────
	mux.HandleFunc("GET  /health",               handleHealth)
	mux.HandleFunc("GET  /metrics",              metricsHandler(cfg.Metrics))
	mux.HandleFunc("GET  /projects",             projectsH.List)
	mux.HandleFunc("GET  /projects/{id}",        projectsH.Get)
	mux.HandleFunc("POST /projects/{id}/start",  projectsH.Start)
	mux.HandleFunc("POST /projects/{id}/stop",   projectsH.Stop)
	mux.HandleFunc("POST /projects/register",    projectsH.Register)
	mux.HandleFunc("GET  /services",             servicesH.List)
	mux.HandleFunc("GET  /events",               eventsH.List)

	// ── Agent routes (Phase 14) ───────────────────────────────────────────────
	mux.HandleFunc("GET  /agents",                  agentsH.List)
	mux.HandleFunc("POST /agents/register",         agentsH.Register)
	mux.HandleFunc("POST /agents/{id}/heartbeat",   agentsH.Heartbeat)
	mux.HandleFunc("GET  /agents/{id}/desired",     agentsH.Desired)
	mux.HandleFunc("POST /agents/{id}/actual",      agentsH.Actual)

	var h http.Handler = mux
	h = middleware.Recovery(h, cfg.Logger)
	h = middleware.Logging(h, cfg.Logger)
	return h
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true,"status":"healthy"}`)) //nolint:errcheck
}

func metricsHandler(m *telemetry.Metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var snap telemetry.Snapshot
		if m != nil {
			snap = m.Snapshot()
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(snap) //nolint:errcheck
	}
}
