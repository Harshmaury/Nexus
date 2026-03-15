// @nexus-project: nexus
// @nexus-path: internal/watcher/watcher.go
// Package watcher monitors filesystem directories and publishes events
// to the bus for downstream consumers to process.
//
// ADR-002 implementation — Nexus owns filesystem observation:
//
//   The watcher supports two modes:
//
//   1. Drop mode (existing)
//      Watches the nexus-drop folder.
//      Publishes TopicFileDropped for the intelligence pipeline.
//      No change to existing behaviour.
//
//   2. Workspace mode (ADR-002)
//      Watches the workspace root and its project subdirectories.
//      Publishes workspace event topics so Atlas and Forge can
//      subscribe without running independent watchers.
//
//   Published workspace topics (declared in eventbus/bus.go):
//     TopicWorkspaceFileCreated
//     TopicWorkspaceFileModified
//     TopicWorkspaceFileDeleted
//     TopicWorkspaceUpdated      (debounced batch signal)
//     TopicWorkspaceProjectDetected
//
//   Consumers import topic constants from eventbus — never redefine.
//
// NX-Fix-01: debounce map data race eliminated.
//   Previously, time.AfterFunc callbacks ran in separate goroutines and
//   called delete(dropDebounce, path) concurrently with the event loop
//   reading and writing the same map — a data race under -race.
//
//   Fix: debounceMap wraps each map with a sync.Mutex. The event loop
//   calls Reset (lock → stop old timer → set new timer → unlock).
//   The AfterFunc callback calls Delete (lock → delete key → unlock)
//   before dispatching the handler. All map mutations are serialised.
package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	// debounceDelay is the quiet period for drop-folder events.
	// Editors write files in multiple bursts; we wait for the last write.
	debounceDelay = 300 * time.Millisecond

	// workspaceDebounceDelay is the quiet period for workspace events.
	// A git checkout touches many files at once; batch them into one signal.
	workspaceDebounceDelay = 1 * time.Second

	// maxDropFileSizeBytes is the largest file the drop pipeline will process.
	maxDropFileSizeBytes = 10 * 1024 * 1024 // 10 MB

	// project manifest filenames used to detect new projects.
	nexusManifest  = ".nexus.yaml"
	goModManifest  = "go.mod"
	nodeManifest   = "package.json"
	cargoManifest  = "Cargo.toml"
	pyManifest     = "pyproject.toml"
	dotnetManifest = ".csproj"
)

// projectManifests maps manifest filenames to the detector label used in
// TopicWorkspaceProjectDetected payloads.
var projectManifests = map[string]string{
	nexusManifest:  "nexus.yaml",
	goModManifest:  "go.mod",
	nodeManifest:   "package.json",
	cargoManifest:  "Cargo.toml",
	pyManifest:     "pyproject.toml",
	dotnetManifest: ".csproj",
}

// ── DEBOUNCE MAP ─────────────────────────────────────────────────────────────

// debounceMap is a goroutine-safe map of path → pending timer.
//
// The event loop (single goroutine) calls Reset to arm or re-arm a timer.
// The AfterFunc callback (separate goroutine) calls Delete before
// dispatching, ensuring the map entry is removed under the lock before
// any handler runs.
type debounceMap struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
}

func newDebounceMap() *debounceMap {
	return &debounceMap{timers: make(map[string]*time.Timer)}
}

// Reset stops any existing timer for path, then sets a new one that calls fn
// after delay. Safe to call from the event loop goroutine only.
func (d *debounceMap) Reset(path string, delay time.Duration, fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.timers[path]; ok {
		t.Stop()
	}
	d.timers[path] = time.AfterFunc(delay, func() {
		d.Delete(path)
		fn()
	})
}

// Delete removes the timer entry for path. Called by the AfterFunc callback.
func (d *debounceMap) Delete(path string) {
	d.mu.Lock()
	delete(d.timers, path)
	d.mu.Unlock()
}

// ── WATCH MODE ────────────────────────────────────────────────────────────────

// WatchMode controls which event set a directory target publishes.
type WatchMode int

const (
	// WatchModeDropFolder publishes drop intelligence topics.
	WatchModeDropFolder WatchMode = iota

	// WatchModeWorkspace publishes workspace change topics (ADR-002).
	WatchModeWorkspace
)

// WatchTarget is a directory + mode pair passed to NewMulti.
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

// New creates a Watcher for a single drop-folder directory.
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

	dropDebounce      := newDebounceMap()
	workspaceDebounce := newDebounceMap()

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
			mode    := w.modeForPath(absPath)

			if isDir(absPath) || isHidden(absPath) {
				continue
			}

			switch mode {
			case WatchModeDropFolder:
				if !isCreateOrWrite(event.Op) {
					continue
				}
				dropDebounce.Reset(absPath, debounceDelay, func() {
					w.handleDropFile(absPath)
				})

			case WatchModeWorkspace:
				op := event.Op // capture for closure
				workspaceDebounce.Reset(absPath, workspaceDebounceDelay, func() {
					w.handleWorkspaceEvent(absPath, op)
				})
			}
		}
	}
}

// ── DROP HANDLER ─────────────────────────────────────────────────────────────

func (w *Watcher) handleDropFile(absPath string) {
	info, err := os.Stat(absPath)
	if err != nil {
		return
	}
	if info.IsDir() {
		return
	}
	if info.Size() > maxDropFileSizeBytes {
		_ = w.events.SystemAlert("warn",
			fmt.Sprintf("drop: skipping oversized file %s (%d bytes)",
				info.Name(), info.Size()),
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
	now  := time.Now().UTC()
	name := filepath.Base(absPath)
	ext  := filepath.Ext(absPath)

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

	switch {
	case isCreateOrWrite(op) && sizeBytes > 0:
		if op&fsnotify.Create != 0 {
			w.bus.PublishAsync(eventbus.TopicWorkspaceFileCreated, "workspace", payload)
		} else {
			w.bus.PublishAsync(eventbus.TopicWorkspaceFileModified, "workspace", payload)
		}
	case op&fsnotify.Remove != 0 || op&fsnotify.Rename != 0:
		payload.SizeBytes = 0
		w.bus.PublishAsync(eventbus.TopicWorkspaceFileDeleted, "workspace", payload)
	}

	w.bus.PublishAsync(eventbus.TopicWorkspaceUpdated, "workspace",
		eventbus.WorkspaceUpdatedPayload{
			WatchDir: filepath.Dir(absPath),
			EventAt:  now,
		},
	)

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
		sep := string(filepath.Separator)
		if strings.HasPrefix(dir+sep, t.Dir+sep) || dir == t.Dir {
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
