// @nexus-project: nexus
// @nexus-path: internal/daemon/server.go
// Server listens on a Unix domain socket and handles requests from the engx CLI.
// Protocol: newline-delimited JSON. One Request → one Response per connection.
//
// Fix 03 changes:
//   - store field changed from *state.Store → state.Storer (consistent with Fix 02)
//   - ServerConfig.Store changed from *state.Store → state.Storer
//   - Added CmdRegisterProject command + RegisterProjectParams
//     CLI register command now routes through daemon instead of writing SQLite directly
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

// ── CONSTANTS ─────────────────────────────────────────────────────────────────

const (
	DefaultSocketPath    = "/tmp/engx.sock"
	requestReadTimeout   = 10 * time.Second
	responseWriteTimeout = 10 * time.Second
	maxRequestBytes      = 64 * 1024 // 64KB
)

// ── PROTOCOL ──────────────────────────────────────────────────────────────────

// Command is the action the CLI wants the daemon to perform.
type Command string

const (
	CmdProjectStart     Command = "project.start"
	CmdProjectStop      Command = "project.stop"
	CmdProjectStatus    Command = "project.status"
	CmdProjectList      Command = "project.list"
	CmdServicesList     Command = "services.list"
	CmdEventsList       Command = "events.list"
	CmdRegisterProject  Command = "project.register"
	CmdPing             Command = "ping"
)

// Request is a message from the CLI to the daemon.
type Request struct {
	ID      string          `json:"id"`
	Command Command         `json:"command"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a message from the daemon back to the CLI.
type Response struct {
	ID      string          `json:"id"`
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
	Elapsed string          `json:"elapsed,omitempty"`
}

// ── PARAM TYPES ───────────────────────────────────────────────────────────────

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

// RegisterProjectParams are the params for CmdRegisterProject.
// All fields mirror state.Project — the daemon owns the write.
type RegisterProjectParams struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Language    string `json:"language"`
	ProjectType string `json:"project_type"`
	ConfigJSON  string `json:"config_json"`
}

// ── SERVER ────────────────────────────────────────────────────────────────────

// Server listens on a Unix socket and dispatches CLI requests.
type Server struct {
	socketPath  string
	store       state.Storer
	bus         *eventbus.Bus
	projectCtrl *controllers.ProjectController
	listener    net.Listener
}

// ServerConfig holds all dependencies for the Server.
type ServerConfig struct {
	SocketPath  string
	Store       state.Storer
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

// ── RUN ───────────────────────────────────────────────────────────────────────

// Run starts the Unix socket listener and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.socketPath, err)
	}
	s.listener = ln
	defer ln.Close()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return fmt.Errorf("accept: %w", err)
		}
		go s.handleConn(conn)
	}
}

// ── CONNECTION HANDLER ────────────────────────────────────────────────────────

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	start := time.Now()

	conn.SetReadDeadline(time.Now().Add(requestReadTimeout))
	limited := io.LimitReader(conn, maxRequestBytes)

	var req Request
	if err := json.NewDecoder(limited).Decode(&req); err != nil {
		s.writeError(conn, "", fmt.Sprintf("decode request: %v", err), start)
		return
	}

	data, err := s.dispatch(req)
	conn.SetWriteDeadline(time.Now().Add(responseWriteTimeout))

	if err != nil {
		s.writeError(conn, req.ID, err.Error(), start)
		return
	}

	resp := Response{
		ID:      req.ID,
		OK:      true,
		Data:    data,
		Elapsed: time.Since(start).Round(time.Millisecond).String(),
	}
	json.NewEncoder(conn).Encode(resp) //nolint:errcheck — best-effort write
}

func (s *Server) writeError(conn net.Conn, id, msg string, start time.Time) {
	resp := Response{
		ID:      id,
		OK:      false,
		Error:   msg,
		Elapsed: time.Since(start).Round(time.Millisecond).String(),
	}
	json.NewEncoder(conn).Encode(resp) //nolint:errcheck
}

// ── DISPATCH ─────────────────────────────────────────────────────────────────

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

	case CmdRegisterProject:
		// The daemon owns all writes. CLI sends parsed manifest fields;
		// daemon calls store.RegisterProject — single writer, no dual-SQLite risk.
		var params RegisterProjectParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if params.ID == "" || params.Name == "" {
			return nil, fmt.Errorf("id and name are required")
		}
		project := &state.Project{
			ID:          params.ID,
			Name:        params.Name,
			Path:        params.Path,
			Language:    params.Language,
			ProjectType: params.ProjectType,
			ConfigJSON:  params.ConfigJSON,
		}
		if err := s.store.RegisterProject(project); err != nil {
			return nil, fmt.Errorf("register project: %w", err)
		}
		return jsonMarshal(map[string]string{
			"id":   params.ID,
			"name": params.Name,
		})

	default:
		return nil, fmt.Errorf("unknown command %q", req.Command)
	}
}

// ── HELPERS ───────────────────────────────────────────────────────────────────

func jsonMarshal(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	return b, nil
}
