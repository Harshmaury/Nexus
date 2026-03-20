// @nexus-project: nexus
// @nexus-path: internal/daemon/server.go
// NX-Fix-02: local moveFile removed — delegated to pkg/osutil.MoveFile.
//
// Phase 13 addition:
//   CmdDropTrain — reads download_log (action=moved or approved),
//   passes training examples to the Classifier, saves the model.
//   The Classifier is injected into ServerConfig so the daemon's
//   single Classifier instance is shared between train and detect.
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
	"github.com/Harshmaury/Nexus/internal/intelligence"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/pkg/osutil"
)

// ── CONSTANTS ─────────────────────────────────────────────────────────────────

const (
	DefaultSocketPath    = "/tmp/engx.sock"
	requestReadTimeout   = 10 * time.Second
	responseWriteTimeout = 10 * time.Second
	maxRequestBytes      = 64 * 1024
	quarantinePrefix     = "UNROUTED__"

	// maxTrainingRows caps how many download_log rows are read for training.
	// Keeps training fast; older examples matter less than recent ones.
	maxTrainingRows = 2000
)

// ── PROTOCOL ──────────────────────────────────────────────────────────────────

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
	CmdDropTrain       Command = "drop.train"
	CmdPing            Command = "ping"
)

type Request struct {
	ID      string          `json:"id"`
	Command Command         `json:"command"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	ID      string          `json:"id"`
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
	Elapsed string          `json:"elapsed,omitempty"`
}

// ── PARAM TYPES ───────────────────────────────────────────────────────────────

type ProjectStartParams  struct{ ProjectID string `json:"project_id"` }
type ProjectStopParams   struct{ ProjectID string `json:"project_id"` }
type ProjectStatusParams struct{ ProjectID string `json:"project_id"` }
type EventsListParams    struct{ Limit     int    `json:"limit"` }

type RegisterProjectParams struct {
	ID, Name, Path, Language, ProjectType, ConfigJSON string
}

type DropApproveParams struct{ FilePath string `json:"file_path"` }
type DropRejectParams  struct{ FilePath string `json:"file_path"` }

type PendingApproval struct {
	FilePath    string  `json:"file_path"`
	FileName    string  `json:"file_name"`
	ProjectID   string  `json:"project_id"`
	Destination string  `json:"destination"`
	Confidence  float64 `json:"confidence"`
	Method      string  `json:"method"`
}

// ── SERVER ────────────────────────────────────────────────────────────────────

type Server struct {
	socketPath       string
	store            state.Storer
	bus              *eventbus.Bus
	projectCtrl      *controllers.ProjectController
	classifier       *intelligence.Classifier
	listener         net.Listener
	mu               sync.RWMutex
	pendingApprovals map[string]eventbus.DropApprovalPayload
	pendingSubID     string
}

type ServerConfig struct {
	SocketPath  string
	Store       state.Storer
	Bus         *eventbus.Bus
	ProjectCtrl *controllers.ProjectController
	Classifier  *intelligence.Classifier // injected — same instance as Detector uses
}

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
		classifier:       cfg.Classifier,
		pendingApprovals: make(map[string]eventbus.DropApprovalPayload),
	}
	s.pendingSubID = cfg.Bus.Subscribe(eventbus.TopicDropPendingApproval, s.onDropPendingApproval)
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

	go func() { <-ctx.Done(); ln.Close() }()

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

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	start := time.Now()

	conn.SetReadDeadline(time.Now().Add(requestReadTimeout))
	var req Request
	if err := json.NewDecoder(io.LimitReader(conn, maxRequestBytes)).Decode(&req); err != nil {
		s.writeError(conn, "", fmt.Sprintf("decode: %v", err), start)
		return
	}

	data, err := s.dispatch(req)
	conn.SetWriteDeadline(time.Now().Add(responseWriteTimeout))
	if err != nil {
		s.writeError(conn, req.ID, err.Error(), start)
		return
	}
	json.NewEncoder(conn).Encode(Response{ //nolint:errcheck
		ID: req.ID, OK: true, Data: data,
		Elapsed: time.Since(start).Round(time.Millisecond).String(),
	})
}

func (s *Server) writeError(conn net.Conn, id, msg string, start time.Time) {
	json.NewEncoder(conn).Encode(Response{ //nolint:errcheck
		ID: id, OK: false, Error: msg,
		Elapsed: time.Since(start).Round(time.Millisecond).String(),
	})
}

// ── DISPATCH ─────────────────────────────────────────────────────────────────

func (s *Server) dispatch(req Request) (json.RawMessage, error) {
	switch req.Command {

	case CmdPing:
		return jsonMarshal(map[string]string{"status": "ok", "version": "0.1.0"})

	case CmdProjectStart:
		var p ProjectStartParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.ProjectID == "" {
			return nil, fmt.Errorf("project_id required")
		}
		count, err := s.projectCtrl.StartProject(p.ProjectID)
		if err != nil {
			return nil, err
		}
		return jsonMarshal(map[string]any{"project_id": p.ProjectID, "queued": count})

	case CmdProjectStop:
		var p ProjectStopParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.ProjectID == "" {
			return nil, fmt.Errorf("project_id required")
		}
		count, err := s.projectCtrl.StopProject(p.ProjectID)
		if err != nil {
			return nil, err
		}
		return jsonMarshal(map[string]any{"project_id": p.ProjectID, "queued": count})

	case CmdProjectStatus:
		var p ProjectStatusParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.ProjectID == "" {
			statuses, err := s.projectCtrl.GetAllProjectsStatus()
			if err != nil {
				return nil, err
			}
			return jsonMarshal(statuses)
		}
		status, err := s.projectCtrl.GetProjectStatus(p.ProjectID)
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
		var p EventsListParams
		if req.Params != nil {
			_ = json.Unmarshal(req.Params, &p)
		}
		if p.Limit <= 0 {
			p.Limit = 20
		}
		events, err := s.store.GetRecentEvents(p.Limit)
		if err != nil {
			return nil, fmt.Errorf("get events: %w", err)
		}
		return jsonMarshal(events)

	case CmdRegisterProject:
		var p RegisterProjectParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.ID == "" || p.Name == "" {
			return nil, fmt.Errorf("id and name required")
		}
		if err := s.store.RegisterProject(&state.Project{
			ID: p.ID, Name: p.Name, Path: p.Path,
			Language: p.Language, ProjectType: p.ProjectType, ConfigJSON: p.ConfigJSON,
		}); err != nil {
			return nil, fmt.Errorf("register project: %w", err)
		}
		return jsonMarshal(map[string]string{"id": p.ID, "name": p.Name})

	case CmdDropApprove:
		return s.handleDropApprove(req.Params)

	case CmdDropReject:
		return s.handleDropReject(req.Params)

	case CmdDropPending:
		return s.handleDropPending()

	case CmdDropTrain:
		return s.handleDropTrain()

	case CmdDeregisterProject:
		var p DeregisterProjectParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.ProjectID == "" {
			return nil, fmt.Errorf("project_id required")
		}
		if _, err := s.projectCtrl.StopProject(p.ProjectID); err != nil {
			return nil, fmt.Errorf("stop project before deregister: %w", err)
		}
		servicesRemoved, err := s.store.DeleteServicesByProject(p.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("delete services: %w", err)
		}
		if err := s.store.DeregisterProject(p.ProjectID); err != nil {
			return nil, err
		}
		return jsonMarshal(map[string]any{
			"project_id":       p.ProjectID,
			"services_removed": servicesRemoved,
		})

	default:
		return nil, fmt.Errorf("unknown command %q", req.Command)
	}
}

// ── DROP HANDLERS ─────────────────────────────────────────────────────────────

func (s *Server) handleDropApprove(raw json.RawMessage) (json.RawMessage, error) {
	var p DropApproveParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.FilePath == "" {
		return nil, fmt.Errorf("file_path required")
	}
	payload, ok := s.popPending(p.FilePath)
	if !ok {
		return nil, fmt.Errorf("no pending approval for %q", filepath.Base(p.FilePath))
	}
	if err := osutil.MoveFile(payload.FilePath, payload.Destination); err != nil {
		return nil, fmt.Errorf("move file: %w", err)
	}
	s.bus.Publish(eventbus.TopicFileRouted, "drop", eventbus.FileRoutedPayload{
		OriginalName: filepath.Base(payload.FilePath),
		RenamedTo:    filepath.Base(payload.Destination),
		Project:      payload.ProjectID, Destination: payload.Destination,
		Method: payload.Method + "+approved", Confidence: payload.Confidence,
	})
	return jsonMarshal(map[string]string{
		"file": filepath.Base(payload.FilePath), "destination": payload.Destination,
		"project": payload.ProjectID, "action": "approved",
	})
}

func (s *Server) handleDropReject(raw json.RawMessage) (json.RawMessage, error) {
	var p DropRejectParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if p.FilePath == "" {
		return nil, fmt.Errorf("file_path required")
	}
	payload, ok := s.popPending(p.FilePath)
	if !ok {
		return nil, fmt.Errorf("no pending approval for %q", filepath.Base(p.FilePath))
	}
	taggedName := quarantinePrefix + filepath.Base(payload.FilePath)
	taggedPath := filepath.Join(filepath.Dir(payload.FilePath), taggedName)
	if err := os.Rename(payload.FilePath, taggedPath); err != nil {
		return nil, fmt.Errorf("tag file: %w", err)
	}
	s.bus.Publish(eventbus.TopicFileQuarantined, "drop", eventbus.FileDropPayload{
		OriginalPath: payload.FilePath, FileName: taggedName,
	})
	return jsonMarshal(map[string]string{
		"file": filepath.Base(payload.FilePath), "tagged": taggedName, "action": "rejected",
	})
}

func (s *Server) handleDropPending() (json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries := make([]PendingApproval, 0, len(s.pendingApprovals))
	for _, p := range s.pendingApprovals {
		entries = append(entries, PendingApproval{
			FilePath: p.FilePath, FileName: filepath.Base(p.FilePath),
			ProjectID: p.ProjectID, Destination: p.Destination,
			Confidence: p.Confidence, Method: p.Method,
		})
	}
	return jsonMarshal(entries)
}

// handleDropTrain reads download_log, extracts training examples from
// confirmed routes (action='moved' or 'approved'), trains the classifier,
// and returns a summary of what was learned.
func (s *Server) handleDropTrain() (json.RawMessage, error) {
	if s.classifier == nil {
		return nil, fmt.Errorf("classifier not initialised in daemon — restart engxd")
	}

	logs, err := s.store.GetRecentDownloads(maxTrainingRows)
	if err != nil {
		return nil, fmt.Errorf("read download_log: %w", err)
	}

	// Only learn from confirmed positive routes.
	// Rejected files are excluded — they represent wrong routing decisions.
	examples := make([]intelligence.TrainingExample, 0, len(logs))
	for _, dl := range logs {
		if dl.Action != "moved" && dl.Action != "approved" {
			continue
		}
		if dl.OriginalName == "" || dl.Project == "" {
			continue
		}
		examples = append(examples, intelligence.TrainingExample{
			FileName:  dl.OriginalName,
			ProjectID: dl.Project,
		})
	}

	count, err := s.classifier.Train(examples, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("train classifier: %w", err)
	}

	info := s.classifier.ModelInfo()
	if info == nil {
		info = map[string]any{}
	}
	info["examples_used"] = count
	info["total_in_log"] = len(logs)

	return jsonMarshal(info)
}

// ── BUS SUBSCRIBER ────────────────────────────────────────────────────────────

func (s *Server) onDropPendingApproval(event eventbus.Event) error {
	payload, ok := event.Payload.(eventbus.DropApprovalPayload)
	if !ok {
		return fmt.Errorf("unexpected payload type for TopicDropPendingApproval")
	}
	s.mu.Lock()
	s.pendingApprovals[payload.FilePath] = payload
	s.mu.Unlock()
	return nil
}

func (s *Server) popPending(filePath string) (eventbus.DropApprovalPayload, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pendingApprovals[filePath]
	if ok {
		delete(s.pendingApprovals, filePath)
	}
	return p, ok
}

// ── FILE MOVE ─────────────────────────────────────────────────────────────────


func jsonMarshal(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return b, nil
}
