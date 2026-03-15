// @nexus-project: nexus
// @nexus-path: internal/daemon/server.go
// Server listens on a Unix domain socket and handles requests from the engx CLI.
// Protocol: newline-delimited JSON. One Request → one Response per connection.
//
// Phase 10 additions:
//   - Server subscribes to TopicDropPendingApproval on construction.
//     Payloads are stored in pendingApprovals map[filePath]DropApprovalPayload.
//     This map is the bridge between the intelligence pipeline (which publishes
//     the event) and the CLI (which sends approve/reject via the socket).
//
//   - CmdDropApprove: moves the file to its resolved destination, publishes
//     TopicFileRouted, removes entry from pending map.
//
//   - CmdDropReject: renames the file with UNROUTED__ prefix in-place,
//     publishes TopicFileQuarantined, removes entry from pending map.
//
//   - CmdDropPending: returns all currently pending approval entries so the
//     CLI can list them with confidence scores and destinations.
//
//   Pending map is in-memory only — if the daemon restarts, pending files
//   remain in the drop folder and are re-detected by the watcher on next
//   write event. This is safe: the file is never moved until explicitly approved.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
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
	quarantinePrefix     = "UNROUTED__"
)

// ── PROTOCOL ──────────────────────────────────────────────────────────────────

// Command is the action the CLI wants the daemon to perform.
type Command string

const (
	CmdProjectStart    Command = "project.start"
	CmdProjectStop     Command = "project.stop"
	CmdProjectStatus   Command = "project.status"
	CmdProjectList     Command = "project.list"
	CmdServicesList    Command = "services.list"
	CmdEventsList      Command = "events.list"
	CmdRegisterProject Command = "project.register"
	CmdDropApprove     Command = "drop.approve"
	CmdDropReject      Command = "drop.reject"
	CmdDropPending     Command = "drop.pending"
	CmdPing            Command = "ping"
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

type ProjectStartParams struct {
	ProjectID string `json:"project_id"`
}

type ProjectStopParams struct {
	ProjectID string `json:"project_id"`
}

type ProjectStatusParams struct {
	ProjectID string `json:"project_id"`
}

type EventsListParams struct {
	Limit int `json:"limit"`
}

type RegisterProjectParams struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Language    string `json:"language"`
	ProjectType string `json:"project_type"`
	ConfigJSON  string `json:"config_json"`
}

// DropApproveParams are the params for CmdDropApprove.
// FilePath is the absolute path of the file currently in the drop folder.
type DropApproveParams struct {
	FilePath string `json:"file_path"`
}

// DropRejectParams are the params for CmdDropReject.
type DropRejectParams struct {
	FilePath string `json:"file_path"`
}

// PendingApproval is one entry returned by CmdDropPending.
type PendingApproval struct {
	FilePath    string  `json:"file_path"`
	FileName    string  `json:"file_name"`
	ProjectID   string  `json:"project_id"`
	Destination string  `json:"destination"`
	Confidence  float64 `json:"confidence"`
	Method      string  `json:"method"`
}

// ── SERVER ────────────────────────────────────────────────────────────────────

// Server listens on a Unix socket and dispatches CLI requests.
type Server struct {
	socketPath  string
	store       state.Storer
	bus         *eventbus.Bus
	projectCtrl *controllers.ProjectController
	listener    net.Listener

	// pendingApprovals holds files waiting for engx drop approve/reject.
	// Keyed by absolute file path. Protected by mu.
	mu                 sync.RWMutex
	pendingApprovals   map[string]eventbus.DropApprovalPayload
	pendingSubID       string
}

// ServerConfig holds all dependencies for the Server.
type ServerConfig struct {
	SocketPath  string
	Store       state.Storer
	Bus         *eventbus.Bus
	ProjectCtrl *controllers.ProjectController
}

// NewServer creates a Server and subscribes to TopicDropPendingApproval
// so pending files are tracked automatically as the pipeline runs.
func NewServer(cfg ServerConfig) *Server {
	socketPath := cfg.SocketPath
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}

	s := &Server{
		socketPath:       socketPath,
		store:            cfg.Store,
		bus:              cfg.Bus,
		projectCtrl:      cfg.ProjectCtrl,
		pendingApprovals: make(map[string]eventbus.DropApprovalPayload),
	}

	// Subscribe to pending approval events from the intelligence pipeline.
	s.pendingSubID = cfg.Bus.Subscribe(
		eventbus.TopicDropPendingApproval,
		s.onDropPendingApproval,
	)

	return s
}

// ── RUN ───────────────────────────────────────────────────────────────────────

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
	defer s.bus.Unsubscribe(s.pendingSubID)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
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
	json.NewEncoder(conn).Encode(resp) //nolint:errcheck
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
		return jsonMarshal(map[string]any{"project_id": params.ProjectID, "queued": count})

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
		return jsonMarshal(map[string]any{"project_id": params.ProjectID, "queued": count})

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
		return jsonMarshal(map[string]string{"id": params.ID, "name": params.Name})

	case CmdDropApprove:
		return s.handleDropApprove(req.Params)

	case CmdDropReject:
		return s.handleDropReject(req.Params)

	case CmdDropPending:
		return s.handleDropPending()

	default:
		return nil, fmt.Errorf("unknown command %q", req.Command)
	}
}

// ── DROP HANDLERS ─────────────────────────────────────────────────────────────

// handleDropApprove moves the file to its resolved destination.
func (s *Server) handleDropApprove(raw json.RawMessage) (json.RawMessage, error) {
	var params DropApproveParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if params.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	payload, ok := s.popPending(params.FilePath)
	if !ok {
		return nil, fmt.Errorf("no pending approval for %q — file may have already been approved or rejected", filepath.Base(params.FilePath))
	}

	if err := moveFile(payload.FilePath, payload.Destination); err != nil {
		return nil, fmt.Errorf("move file: %w", err)
	}

	s.bus.Publish(eventbus.TopicFileRouted, "drop", eventbus.FileRoutedPayload{
		OriginalName: filepath.Base(payload.FilePath),
		RenamedTo:    filepath.Base(payload.Destination),
		Project:      payload.ProjectID,
		Destination:  payload.Destination,
		Method:       payload.Method + "+approved",
		Confidence:   payload.Confidence,
	})

	return jsonMarshal(map[string]string{
		"file":        filepath.Base(payload.FilePath),
		"destination": payload.Destination,
		"project":     payload.ProjectID,
		"action":      "approved",
	})
}

// handleDropReject renames the file with UNROUTED__ prefix and leaves it in place.
func (s *Server) handleDropReject(raw json.RawMessage) (json.RawMessage, error) {
	var params DropRejectParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if params.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	payload, ok := s.popPending(params.FilePath)
	if !ok {
		return nil, fmt.Errorf("no pending approval for %q", filepath.Base(params.FilePath))
	}

	dir := filepath.Dir(payload.FilePath)
	taggedName := quarantinePrefix + filepath.Base(payload.FilePath)
	taggedPath := filepath.Join(dir, taggedName)

	if err := os.Rename(payload.FilePath, taggedPath); err != nil {
		return nil, fmt.Errorf("tag file: %w", err)
	}

	s.bus.Publish(eventbus.TopicFileQuarantined, "drop", eventbus.FileDropPayload{
		OriginalPath: payload.FilePath,
		FileName:     taggedName,
	})

	return jsonMarshal(map[string]string{
		"file":    filepath.Base(payload.FilePath),
		"tagged":  taggedName,
		"action":  "rejected",
	})
}

// handleDropPending lists all files currently awaiting approval.
func (s *Server) handleDropPending() (json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]PendingApproval, 0, len(s.pendingApprovals))
	for _, p := range s.pendingApprovals {
		entries = append(entries, PendingApproval{
			FilePath:    p.FilePath,
			FileName:    filepath.Base(p.FilePath),
			ProjectID:   p.ProjectID,
			Destination: p.Destination,
			Confidence:  p.Confidence,
			Method:      p.Method,
		})
	}
	return jsonMarshal(entries)
}

// ── BUS SUBSCRIBER ────────────────────────────────────────────────────────────

// onDropPendingApproval stores a pending approval payload when the
// intelligence pipeline cannot auto-route a file (confidence 0.40–0.79).
func (s *Server) onDropPendingApproval(event eventbus.Event) error {
	payload, ok := event.Payload.(eventbus.DropApprovalPayload)
	if !ok {
		return fmt.Errorf("server: unexpected payload type for TopicDropPendingApproval")
	}

	s.mu.Lock()
	s.pendingApprovals[payload.FilePath] = payload
	s.mu.Unlock()

	return nil
}

// popPending retrieves and removes a pending approval by file path.
func (s *Server) popPending(filePath string) (eventbus.DropApprovalPayload, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	payload, ok := s.pendingApprovals[filePath]
	if ok {
		delete(s.pendingApprovals, filePath)
	}
	return payload, ok
}

// ── FILE MOVE ─────────────────────────────────────────────────────────────────

// moveFile moves src to dst, creating parent directories as needed.
// Falls back to copy+delete if src and dst are on different filesystems.
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Cross-filesystem: copy then delete.
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	defer out.Close()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("write: %w", writeErr)
			}
		}
		if readErr != nil {
			break
		}
	}
	return os.Remove(src)
}

// ── HELPERS ───────────────────────────────────────────────────────────────────

func jsonMarshal(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	return b, nil
}
