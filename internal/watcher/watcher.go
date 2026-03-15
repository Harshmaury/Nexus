// @nexus-project: nexus
// @nexus-path: internal/watcher/watcher.go
// Package watcher monitors filesystem directories and publishes events
// to the bus for downstream consumers to process.
//
// ADR-002 implementation — Nexus owns filesystem observation:
//
//   The watcher now supports two modes:
//
//   1. Drop mode (existing)
//      Watches the nexus-drop folder.
//      Publishes TopicFileDropped for the intelligence pipeline.
//      No change to existing behaviour.
//
//   2. Workspace mode (new — ADR-002)
//      Watches the workspace root and its project subdirectories.
//      Publishes workspace event topics so Atlas and Forge can
//      subscribe without running independent watchers.
//
//   Both modes run through the same watcher loop.
//   The WatcherConfig controls which directories are watched and
//   which event set is published for each.
//
//   Published workspace topics (declared in eventbus/bus.go):
//     TopicWorkspaceFileCreated
//     TopicWorkspaceFileModified
//     TopicWorkspaceFileDeleted
//     TopicWorkspaceUpdated      (debounced batch signal)
//     TopicWorkspaceProjectDetected
//
//   Consumers import topic constants from eventbus — never redefine.
package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	// Drop watcher debounce — editors write files in multiple events.
	debounceDelay = 300 * time.Millisecond

	// Workspace watcher debounce — longer window to batch related changes
	// (e.g. a git checkout touches many files at once).
	workspaceDebounceDelay = 1 * time.Second

	// Maximum file size processed by the drop pipeline.
	maxFileSizeBytes = 10 * 1024 * 1024 // 10MB

	// project manifest filenames used to detect new projects.
	nexusManifest  = ".nexus.yaml"
	goModManifest  = "go.mod"
	nodeManifest   = "package.json"
	cargoManifest  = "Cargo.toml"
	pyManifest     = "pyproject.toml"
	dotnetManifest = ".csproj"
)

// projectManifests is the set of filenames that indicate a project root.
var projectManifests = map[string]string{
	nexusManifest:  "nexus.yaml",
	goModManifest:  "go.mod",
	nodeManifest:   "package.json",
	cargoManifest:  "Cargo.toml",
	pyManifest:     "pyproject.toml",
	dotnetManifest: ".csproj",
}

// ── WATCHER CONFIG ────────────────────────────────────────────────────────────

// WatchMode controls what a directory watch target publishes.
type WatchMode int

const (
	// WatchModeDropFolder publishes drop intelligence topics (existing behaviour).
	WatchModeDropFolder WatchMode = iota

	// WatchModeWorkspace publishes workspace change topics (ADR-002).
	WatchModeWorkspace
)

// WatchTarget is a directory + mode pair.
type WatchTarget struct {
	Dir  string
	Mode WatchMode
}

// ── WATCHER ──────────────────────────────────────────────────────────────────

// Watcher monitors one or more directories and publishes events to the bus.
type Watcher struct {
	targets []WatchTarget
	bus     *eventbus.Bus
	events  *state.EventWriter
}

// New creates a Watcher for a single drop folder directory.
// Preserves backwards compatibility with all existing call sites.
func New(watchDir string, bus *eventbus.Bus, store state.Storer) *Watcher {
	return &Watcher{
		targets: []WatchTarget{{Dir: watchDir, Mode: WatchModeDropFolder}},
		bus:     bus,
		events:  state.NewEventWriter(store, state.SourceDropSystem),
	}
}

// NewMulti creates a Watcher for multiple directories with different modes.
// Used by engxd to watch both the drop folder and the workspace root.
func NewMulti(targets []WatchTarget, bus *eventbus.Bus, store state.Storer) *Watcher {
	return &Watcher{
		targets: targets,
		bus:     bus,
		events:  state.NewEventWriter(store, state.SourceDropSystem),
	}
}

// WatchDir returns the primary watch directory (first target).
// Preserved for backwards compatibility.
func (w *Watcher) WatchDir() string {
	if len(w.targets) > 0 {
		return w.targets[0].Dir
	}
	return ""
}

// ── RUN ──────────────────────────────────────────────────────────────────────

// Run starts watching all configured directories and blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	for _, t := range w.targets {
		if err := os.MkdirAll(t.Dir, 0755); err != nil {
			return fmt.Errorf("create watch dir %s: %w", t.Dir, err)
		}
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create fsnotify watcher: %w", err)
	}
	defer fsw.Close()

	for _, t := range w.targets {
		if err := fsw.Add(t.Dir); err != nil {
			return fmt.Errorf("watch dir %s: %w", t.Dir, err)
		}
	}

	// debounce tracks files pending processing.
	dropDebounce      := make(map[string]*time.Timer)
	workspaceDebounce := make(map[string]*time.Timer)

	for {
		select {
		case <-ctx.Done():
			return nil

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			_ = w.events.SystemAlert("warn",
				fmt.Sprintf("watcher error: %v", err),
				map[string]string{"component": "watcher"},
			)

		case event, ok := <-fsw.Events:
			if !ok {
				return nil
			}

			absPath := filepath.Clean(event.Name)
			mode := w.modeForPath(absPath)

			if isDir(absPath) || isHidden(absPath) {
				continue
			}

			switch mode {
			case WatchModeDropFolder:
				if !isCreateOrWrite(event.Op) {
					continue
				}
				if t, exists := dropDebounce[absPath]; exists {
					t.Stop()
				}
				dropDebounce[absPath] = time.AfterFunc(debounceDelay, func() {
					delete(dropDebounce, absPath)
					w.handleDropFile(absPath)
				})

			case WatchModeWorkspace:
				if t, exists := workspaceDebounce[absPath]; exists {
					t.Stop()
				}
				workspaceDebounce[absPath] = time.AfterFunc(workspaceDebounceDelay, func() {
					delete(workspaceDebounce, absPath)
					w.handleWorkspaceEvent(absPath, event.Op)
				})
			}
		}
	}
}

// ── DROP HANDLER (existing behaviour) ────────────────────────────────────────

func (w *Watcher) handleDropFile(absPath string) {
	info, err := os.Stat(absPath)
	if err != nil {
		return
	}
	if info.IsDir() {
		return
	}
	if info.Size() > maxFileSizeBytes {
		_ = w.events.SystemAlert("warn",
			fmt.Sprintf("drop: skipping oversized file %s (%d bytes)", info.Name(), info.Size()),
			map[string]string{"path": absPath},
		)
		return
	}

	_ = w.events.FileDropped(absPath, info.Name(), info.Size())

	w.bus.PublishAsync(
		eventbus.TopicFileDropped,
		"drop",
		eventbus.FileDropPayload{
			OriginalPath: absPath,
			FileName:     info.Name(),
			SizeBytes:    info.Size(),
			DetectedAt:   time.Now().UTC(),
		},
	)
}

// ── WORKSPACE HANDLER (ADR-002) ───────────────────────────────────────────────

// handleWorkspaceEvent publishes workspace topics for the given filesystem event.
func (w *Watcher) handleWorkspaceEvent(absPath string, op fsnotify.Op) {
	now := time.Now().UTC()
	name := filepath.Base(absPath)
	ext := filepath.Ext(absPath)

	var sizeBytes int64
	if info, err := os.Stat(absPath); err == nil {
		sizeBytes = info.Size()
	}

	payload := eventbus.WorkspaceFilePayload{
		Path:      absPath,
		Name:      name,
		Extension: ext,
		SizeBytes: sizeBytes,
		EventAt:   now,
	}

	// Publish specific file event.
	switch {
	case isCreateOrWrite(op) && sizeBytes > 0:
		// Distinguish create vs modify: if file very recently appeared use Created.
		// For simplicity we use Created for new files and Modified for existing ones.
		// fsnotify.Create maps to Created; fsnotify.Write maps to Modified.
		if op&fsnotify.Create != 0 {
			w.bus.PublishAsync(eventbus.TopicWorkspaceFileCreated, "workspace", payload)
		} else {
			w.bus.PublishAsync(eventbus.TopicWorkspaceFileModified, "workspace", payload)
		}

	case op&fsnotify.Remove != 0 || op&fsnotify.Rename != 0:
		payload.SizeBytes = 0
		w.bus.PublishAsync(eventbus.TopicWorkspaceFileDeleted, "workspace", payload)
	}

	// Always publish the debounced workspace.updated batch signal.
	w.bus.PublishAsync(eventbus.TopicWorkspaceUpdated, "workspace",
		eventbus.WorkspaceUpdatedPayload{
			WatchDir: filepath.Dir(absPath),
			EventAt:  now,
		},
	)

	// Detect project manifests — publish project detected if this is a known
	// manifest file appearing for the first time (create event only).
	if op&fsnotify.Create != 0 {
		if detectedBy, isManifest := projectManifests[name]; isManifest {
			w.bus.PublishAsync(eventbus.TopicWorkspaceProjectDetected, "workspace",
				eventbus.WorkspaceProjectPayload{
					Path:       filepath.Dir(absPath),
					Name:       filepath.Base(filepath.Dir(absPath)),
					DetectedBy: detectedBy,
					DetectedAt: now,
				},
			)
		}
	}
}

// ── MODE LOOKUP ───────────────────────────────────────────────────────────────

// modeForPath returns the WatchMode for the directory containing absPath.
func (w *Watcher) modeForPath(absPath string) WatchMode {
	dir := filepath.Dir(absPath)
	for _, t := range w.targets {
		if strings.HasPrefix(dir+string(filepath.Separator), t.Dir+string(filepath.Separator)) ||
			dir == t.Dir {
			return t.Mode
		}
	}
	return WatchModeDropFolder // safe default
}

// ── HELPERS ──────────────────────────────────────────────────────────────────

func isCreateOrWrite(op fsnotify.Op) bool {
	return op&fsnotify.Create != 0 || op&fsnotify.Write != 0
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isHidden(path string) bool {
	base := filepath.Base(path)
	return len(base) > 0 && base[0] == '.'
}
