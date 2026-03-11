// @nexus-project: nexus
// @nexus-path: internal/daemon/server.go
// Package daemon — Server listens on a Unix domain socket and handles
// requests from the engx CLI. The CLI never talks to the state store directly
// in production — it sends requests to the daemon via this socket.
// Protocol: newline-delimited JSON. Request → Response, one round trip.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/Harshmaury/Nexus/internal/controllers"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	DefaultSocketPath    = "/tmp/engx.sock"
	requestReadTimeout   = 10 * time.Second
	responseWriteTimeout = 10 * time.Second
	maxRequestBytes      = 64 * 1024 // 64KB — no single request should exceed this
)

// ── PROTOCOL ─────────────────────────────────────────────────────────────────

// Command is the action the CLI wants the daemon to perform.
type Command string

const (
	CmdProjectStart  Command = "project.start"
	CmdProjectStop   Command = "project.stop"
	CmdProjectStatus Command = "project.status"
	CmdProjectList   Command = "project.list"
	CmdServicesList  Command = "services.list"
	CmdEventsList    Command = "events.list"
	CmdPing          Command = "ping"
)

// Request is a message from the CLI to the daemon.
type Request struct {
	ID      string          `json:"id"`      // client-generated request ID for correlation
	Command Command         `json:"command"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a message from the daemon back to the CLI.
type Response struct {
	ID      string          `json:"id"`               // mirrors request ID
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
	Elapsed string          `json:"elapsed,omitempty"` // human-readable duration
}

// ProjectStartParams are the params for CmdProjectStart.
type ProjectStartParams struct {
	ProjectID string `json:"project_id"`
}

// ProjectStopParams are the params for CmdProjectStop.
type ProjectStopParams struct {
	ProjectID string `json:"project_id"`
}

// ProjectStatusParams are the params for CmdProjectStatus.
type ProjectStatusParams struct {
	ProjectID string `json:"project_id"` // empty = all projects
}

// EventsListParams are the params for CmdEventsList.
type EventsListParams struct {
	Limit int `json:"limit"` // defaults to 20
}

// ── SERVER ───────────────────────────────────────────────────────────────────

// Server listens on a Unix socket and dispatches CLI requests.
type Server struct {
	socketPath  string
	store       *state.Store
	bus         *eventbus.Bus
	projectCtrl *controllers.ProjectController
	listener    net.Listener
}

// ServerConfig holds all dependencies for the Server.
type ServerConfig struct {
	SocketPath  string
	Store       *state.Store
	Bus         *eventbus.Bus
	ProjectCtrl *controllers.ProjectController
}

// NewServer creates a Server. Call Run to start accepting connections.
func NewServer(cfg ServerConfig) *Server {
	socketPath := cfg.SocketPath
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	return &Server{
		socketPath:  socketPath,
		store:       cfg.Store,
		bus:         cfg.Bus,
		projectCtrl: cfg.ProjectCtrl,
	}
}

// ── RUN ──────────────────────────────────────────────────────────────────────

// Run starts the Unix socket listener and blocks until ctx is cancelled.
// Removes any stale socket file from a previous run before listening.
func (s *Server) Run(ctx context.Context) error {
	// Remove stale socket from previous daemon run.
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket %s: %w", s.socketPath, err)
	}

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.socketPath, err)
	}
	s.listener = listener
	defer s.cleanup()

	// Accept connections until context is cancelled.
	connCh := make(chan net.Conn)
	errCh := make(chan error, 1)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				errCh <- err
				return
			}
			connCh <- conn
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			// Listener closed — normal shutdown.
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		case conn := <-connCh:
			// Each connection handled in its own goroutine.
			go s.handleConnection(conn)
		}
	}
}

// SocketPath returns the path this server is listening on.
func (s *Server) SocketPath() string {
	return s.socketPath
}

// ── CONNECTION HANDLER ────────────────────────────────────────────────────────

// handleConnection reads one request, dispatches it, writes one response.
// Protocol is intentionally simple: one request per connection.
func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	start := time.Now()

	// Read request with timeout.
	_ = conn.SetReadDeadline(time.Now().Add(requestReadTimeout))

	limited := io.LimitReader(conn, maxRequestBytes)
	decoder := json.NewDecoder(limited)

	var req Request
	if err := decoder.Decode(&req); err != nil {
		s.writeResponse(conn, Response{
			OK:    false,
			Error: fmt.Sprintf("decode request: %v", err),
		})
		return
	}

	// Dispatch to handler.
	data, handlerErr := s.dispatch(req)

	elapsed := time.Since(start).Round(time.Millisecond).String()

	if handlerErr != nil {
		s.writeResponse(conn, Response{
			ID:      req.ID,
			OK:      false,
			Error:   handlerErr.Error(),
			Elapsed: elapsed,
		})
		return
	}

	s.writeResponse(conn, Response{
		ID:      req.ID,
		OK:      true,
		Data:    data,
		Elapsed: elapsed,
	})
}

// ── DISPATCH ─────────────────────────────────────────────────────────────────

// dispatch routes a request to the correct handler and returns JSON data.
func (s *Server) dispatch(req Request) (json.RawMessage, error) {
	switch req.Command {

	case CmdPing:
		return jsonMarshal(map[string]string{
			"status":  "ok",
			"version": "0.1.0",
		})

	case CmdProjectStart:
		var params ProjectStartParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if params.ProjectID == "" {
			return nil, fmt.Errorf("project_id is required")
		}
		count, err := s.projectCtrl.StartProject(params.ProjectID)
		if err != nil {
			return nil, err
		}
		return jsonMarshal(map[string]any{
			"project_id": params.ProjectID,
			"queued":     count,
		})

	case CmdProjectStop:
		var params ProjectStopParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if params.ProjectID == "" {
			return nil, fmt.Errorf("project_id is required")
		}
		count, err := s.projectCtrl.StopProject(params.ProjectID)
		if err != nil {
			return nil, err
		}
		return jsonMarshal(map[string]any{
			"project_id": params.ProjectID,
			"queued":     count,
		})

	case CmdProjectStatus:
		var params ProjectStatusParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if params.ProjectID == "" {
			// All projects.
			statuses, err := s.projectCtrl.GetAllProjectsStatus()
			if err != nil {
				return nil, err
			}
			return jsonMarshal(statuses)
		}
		status, err := s.projectCtrl.GetProjectStatus(params.ProjectID)
		if err != nil {
			return nil, err
		}
		return jsonMarshal(status)

	case CmdProjectList:
		projects, err := s.store.GetAllProjects()
		if err != nil {
			return nil, fmt.Errorf("get projects: %w", err)
		}
		return jsonMarshal(projects)

	case CmdServicesList:
		services, err := s.store.GetAllServices()
		if err != nil {
			return nil, fmt.Errorf("get services: %w", err)
		}
		return jsonMarshal(services)

	case CmdEventsList:
		var params EventsListParams
		if req.Params != nil {
			_ = json.Unmarshal(req.Params, &params)
		}
		limit := params.Limit
		if limit <= 0 {
			limit = 20
		}
		events, err := s.store.GetRecentEvents(limit)
		if err != nil {
			return nil, fmt.Errorf("get events: %w", err)
		}
		return jsonMarshal(events)

	default:
		return nil, fmt.Errorf("unknown command %q", req.Command)
	}
}

// ── HELPERS ──────────────────────────────────────────────────────────────────

func (s *Server) writeResponse(conn net.Conn, resp Response) {
	_ = conn.SetWriteDeadline(time.Now().Add(responseWriteTimeout))
	encoder := json.NewEncoder(conn)
	_ = encoder.Encode(resp)
}

func (s *Server) cleanup() {
	if s.listener != nil {
		_ = s.listener.Close()
	}
	_ = os.Remove(s.socketPath)
}

func jsonMarshal(v any) (json.RawMessage, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal response data: %w", err)
	}
	return json.RawMessage(data), nil
}
