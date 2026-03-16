// @nexus-project: nexus
// @nexus-path: internal/intelligence/router.go
// Router applies confidence thresholds to DetectionResults and
// routes files to their destination, prompts the user, or tags and leaves them.
// It owns all routing policy — detector only scores, router decides.
//
// NX-Fix-02: local moveFile / copyFile removed.
//   Both functions are now delegated to pkg/osutil.MoveFile — the single
//   authoritative implementation shared with internal/daemon/server.go.
//   Previously each package had its own copy; a bug fix in one silently
//   left the other broken.
package intelligence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
	"github.com/Harshmaury/Nexus/pkg/osutil"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	autoRouteThreshold = 0.80 // auto-move + notify
	promptThreshold    = 0.40 // publish approval event; CLI handles it
	// below promptThreshold → tag filename + leave in place

	quarantineTag = "UNROUTED__"
)

// ── ROUTE RESULT ─────────────────────────────────────────────────────────────

// RouteAction is what the router decided to do with a file.
type RouteAction string

const (
	RouteActionMoved    RouteAction = "moved"
	RouteActionPrompted RouteAction = "prompted" // pending CLI approval via bus
	RouteActionTagged   RouteAction = "tagged"
	RouteActionSkipped  RouteAction = "skipped"
)

// RouteResult is the full outcome of routing one file.
type RouteResult struct {
	OriginalPath string
	FinalPath    string
	ProjectID    string
	Action       RouteAction
	Confidence   float64
	Method       string
	RoutedAt     time.Time
}

// ── PROJECT RESOLVER ─────────────────────────────────────────────────────────

// ProjectResolver provides the root path for a registered project.
type ProjectResolver interface {
	GetProjectPath(projectID string) (string, error)
}

// ── ROUTER ───────────────────────────────────────────────────────────────────

// Router routes files based on detection confidence.
type Router struct {
	resolver ProjectResolver
	bus      *eventbus.Bus
	events   *state.EventWriter
	notifier Notifier
}

// NewRouter creates a Router with required dependencies.
//
// store is state.Storer (interface) — consistent with all other components.
// notifier handles OS-level desktop notifications — pass NewDefaultNotifier()
// for production, NullNotifier for tests.
func NewRouter(resolver ProjectResolver, bus *eventbus.Bus, store state.Storer, notifier Notifier) *Router {
	if notifier == nil {
		notifier = &NullNotifier{}
	}
	return &Router{
		resolver: resolver,
		bus:      bus,
		events:   state.NewEventWriter(store, state.SourceDropSystem, state.ComponentDrop),
		notifier: notifier,
	}
}

// ── ROUTE ────────────────────────────────────────────────────────────────────

// Route applies routing policy to a detection result.
func (r *Router) Route(ctx context.Context, detection DetectionResult) (RouteResult, error) {
	result := RouteResult{
		OriginalPath: detection.FilePath,
		ProjectID:    detection.ProjectID,
		Confidence:   detection.Confidence,
		Method:       detection.Method,
		RoutedAt:     time.Now().UTC(),
	}

	switch {
	case detection.Confidence >= autoRouteThreshold:
		return r.autoRoute(ctx, detection, result)
	case detection.Confidence >= promptThreshold:
		return r.promptRoute(ctx, detection, result)
	default:
		return r.tagAndLeave(detection, result)
	}
}

// ── AUTO ROUTE ───────────────────────────────────────────────────────────────

func (r *Router) autoRoute(ctx context.Context, detection DetectionResult, result RouteResult) (RouteResult, error) {
	destination, err := r.resolveDestination(detection)
	if err != nil {
		return result, fmt.Errorf("resolve destination: %w", err)
	}

	if err := osutil.MoveFile(detection.FilePath, destination); err != nil {
		return result, fmt.Errorf("move file: %w", err)
	}

	result.FinalPath = destination
	result.Action = RouteActionMoved

	r.notifyTerminal(detection, destination)

	go r.notifier.Notify(
		"Nexus Drop — "+detection.ProjectID,
		fmt.Sprintf("%s → %s (%.0f%%)",
			filepath.Base(detection.FilePath),
			filepath.Base(destination),
			detection.Confidence*100,
		),
	)

	r.bus.Publish(eventbus.TopicFileRouted, "drop", eventbus.FileRoutedPayload{
		OriginalName: filepath.Base(detection.FilePath),
		RenamedTo:    filepath.Base(destination),
		Project:      detection.ProjectID,
		Destination:  destination,
		Method:       detection.Method,
		Confidence:   detection.Confidence,
	})

	_ = r.events.FileRouted(
		fmt.Sprintf("route-%s-%d", detection.ProjectID, time.Now().UnixNano()),
		filepath.Base(detection.FilePath),
		filepath.Base(destination),
		detection.ProjectID,
		destination,
		detection.Method,
		detection.Confidence,
	)

	return result, nil
}

// ── PROMPT ROUTE ─────────────────────────────────────────────────────────────

// promptRoute handles the 0.40–0.79 confidence range.
// Publishes TopicDropPendingApproval so the CLI can present an interactive
// approval prompt. The pipeline returns immediately with RouteActionPrompted.
func (r *Router) promptRoute(ctx context.Context, detection DetectionResult, result RouteResult) (RouteResult, error) {
	destination, err := r.resolveDestination(detection)
	if err != nil {
		return r.tagAndLeave(detection, result)
	}

	r.bus.Publish(eventbus.TopicDropPendingApproval, "drop", eventbus.DropApprovalPayload{
		FilePath:    detection.FilePath,
		ProjectID:   detection.ProjectID,
		Destination: destination,
		Confidence:  detection.Confidence,
		Method:      detection.Method,
	})

	result.FinalPath = destination
	result.Action = RouteActionPrompted
	return result, nil
}

// ── TAG AND LEAVE ────────────────────────────────────────────────────────────

func (r *Router) tagAndLeave(detection DetectionResult, result RouteResult) (RouteResult, error) {
	dir        := filepath.Dir(detection.FilePath)
	base       := filepath.Base(detection.FilePath)
	taggedName := quarantineTag + base
	taggedPath := filepath.Join(dir, taggedName)

	if err := os.Rename(detection.FilePath, taggedPath); err != nil {
		return result, fmt.Errorf("tag file: %w", err)
	}

	result.FinalPath = taggedPath
	result.Action = RouteActionTagged

	fmt.Printf("\n\033[31m[NEXUS DROP]\033[0m Low confidence file tagged\n")
	fmt.Printf("  File:       %s\n", base)
	fmt.Printf("  Tagged as:  %s\n", taggedName)
	fmt.Printf("  Confidence: %.0f%% — could not determine project\n\n", detection.Confidence*100)

	return result, nil
}

// ── HELPERS ──────────────────────────────────────────────────────────────────

func (r *Router) resolveDestination(detection DetectionResult) (string, error) {
	if detection.ProjectID == "" {
		return "", fmt.Errorf("no project detected")
	}

	projectPath, err := r.resolver.GetProjectPath(detection.ProjectID)
	if err != nil {
		return "", fmt.Errorf("get project path for %s: %w", detection.ProjectID, err)
	}

	if detection.TargetPath != "" {
		return filepath.Join(projectPath, detection.TargetPath), nil
	}

	return filepath.Join(projectPath, filepath.Base(detection.FilePath)), nil
}

func (r *Router) notifyTerminal(detection DetectionResult, destination string) {
	fmt.Printf("\n\033[32m[NEXUS DROP]\033[0m Auto-routed\n")
	fmt.Printf("  File:        %s\n", filepath.Base(detection.FilePath))
	fmt.Printf("  Project:     %s\n", detection.ProjectID)
	fmt.Printf("  Destination: %s\n", destination)
	fmt.Printf("  Confidence:  %.0f%%  (%s)\n\n", detection.Confidence*100, detection.Method)
}
