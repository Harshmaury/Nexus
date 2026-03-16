// @nexus-project: nexus
// @nexus-path: internal/intelligence/pipeline.go
// Pipeline is the top-level coordinator for the Drop Intelligence system.
// It subscribes to TopicFileDropped events from the watcher,
// runs detection, renames if needed, routes the file, and logs the result.
// It is the only component that knows about all four stages.
//
// Fix: PipelineConfig.Store is now state.Storer (interface) instead of
// *state.Store (concrete type). Consistent with all other components.
package intelligence

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ── PIPELINE ─────────────────────────────────────────────────────────────────

// Pipeline coordinates the full drop intelligence workflow:
//  1. Receive file drop event
//  2. Detect project + confidence (weighted scoring)
//  3. Rename to canonical format if needed
//  4. Route based on confidence threshold
//  5. Log result to download_log table
type Pipeline struct {
	detector *Detector
	renamer  *Renamer
	router   *Router
	logger   *DropLogger
	bus      *eventbus.Bus
	events   *state.EventWriter
	subID    string
}

// PipelineConfig holds all dependencies for the Pipeline.
// Store is state.Storer (interface) — not *state.Store (concrete type).
type PipelineConfig struct {
	Detector *Detector
	Renamer  *Renamer
	Router   *Router
	Logger   *DropLogger
	Bus      *eventbus.Bus
	Store    state.Storer
}

// NewPipeline creates a Pipeline and subscribes to TopicFileDropped.
func NewPipeline(cfg PipelineConfig) *Pipeline {
	p := &Pipeline{
		detector: cfg.Detector,
		renamer:  cfg.Renamer,
		router:   cfg.Router,
		logger:   cfg.Logger,
		bus:      cfg.Bus,
		events:   state.NewEventWriter(cfg.Store, state.SourceDropSystem, state.ComponentDrop),
	}
	p.subID = cfg.Bus.Subscribe(eventbus.TopicFileDropped, p.onFileDrop)
	return p
}

// Stop unsubscribes from the event bus.
func (p *Pipeline) Stop() {
	p.bus.Unsubscribe(p.subID)
}

// ── EVENT HANDLER ────────────────────────────────────────────────────────────

// onFileDrop is called by the event bus for every new file in the drop folder.
func (p *Pipeline) onFileDrop(event eventbus.Event) error {
	payload, ok := event.Payload.(eventbus.FileDropPayload)
	if !ok {
		return fmt.Errorf("pipeline: unexpected payload type for TopicFileDropped")
	}

	return p.process(context.Background(), payload.OriginalPath)
}

// ── PROCESS ──────────────────────────────────────────────────────────────────

// process runs the full pipeline for one file.
func (p *Pipeline) process(ctx context.Context, filePath string) error {
	// Step 1 — Detect.
	detection := p.detector.Detect(filePath)

	// Step 2 — Rename to canonical format if needed.
	_, feature := ParseCanonicalName(filepath.Base(filePath))
	renameResult, err := p.renamer.Rename(filePath, detection.ProjectID, feature)
	if err != nil {
		_ = p.events.SystemAlert("warn",
			fmt.Sprintf("drop pipeline: rename failed for %s: %v", filepath.Base(filePath), err),
			map[string]string{"path": filePath},
		)
		// Non-fatal — continue with original path.
	} else if renameResult.WasRenamed {
		// Update detection with new path.
		detection.FilePath = renameResult.NewPath
		detection.FileName = renameResult.NewName
	}

	// Step 3 — Route.
	routeResult, err := p.router.Route(ctx, detection)
	if err != nil {
		_ = p.events.SystemAlert("warn",
			fmt.Sprintf("drop pipeline: route failed for %s: %v", detection.FileName, err),
			map[string]string{
				"path":       detection.FilePath,
				"project_id": detection.ProjectID,
				"confidence": fmt.Sprintf("%.2f", detection.Confidence),
			},
		)
		return fmt.Errorf("route %s: %w", detection.FileName, err)
	}

	// Step 4 — Log to download_log.
	if err := p.logger.Log(DropLogEntry{
		OriginalName: filepath.Base(filePath),
		RenamedTo:    routeResult.FinalPath,
		Project:      routeResult.ProjectID,
		Destination:  routeResult.FinalPath,
		Method:       routeResult.Method,
		Confidence:   routeResult.Confidence,
		Action:       string(routeResult.Action),
	}); err != nil {
		// Logging failure is non-fatal — file was already routed successfully.
		_ = p.events.SystemAlert("warn",
			fmt.Sprintf("drop pipeline: log failed for %s: %v", detection.FileName, err),
			map[string]string{"path": detection.FilePath},
		)
	}

	return nil
}
