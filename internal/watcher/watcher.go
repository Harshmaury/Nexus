// @nexus-project: nexus
// @nexus-path: internal/watcher/watcher.go
// Package watcher monitors the nexus-drop folder for new files
// and publishes TopicFileDropped events to the bus for the intelligence
// pipeline to process. It has no detection logic — pure observation only.
//
// Fix: New() now accepts state.Storer (interface) instead of *state.Store
// (concrete type). Consistent with every other component. Enables testing
// with mock stores without needing a real SQLite database.
package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/Harshmaury/Nexus/internal/eventbus"
	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	// Debounce delay — some editors write files in multiple events.
	// We wait this long after the last event before treating file as stable.
	debounceDelay = 300 * time.Millisecond

	// Only files smaller than this are processed — avoids huge binaries.
	maxFileSizeBytes = 10 * 1024 * 1024 // 10MB
)

// ── WATCHER ──────────────────────────────────────────────────────────────────

// Watcher monitors a directory and publishes file drop events.
type Watcher struct {
	watchDir string
	bus      *eventbus.Bus
	events   *state.EventWriter
}

// New creates a Watcher for the given directory.
// store is state.Storer (interface) — not *state.Store (concrete type).
func New(watchDir string, bus *eventbus.Bus, store state.Storer) *Watcher {
	return &Watcher{
		watchDir: watchDir,
		bus:      bus,
		events:   state.NewEventWriter(store, state.SourceDropSystem),
	}
}

// ── RUN ──────────────────────────────────────────────────────────────────────

// Run starts watching the drop directory and blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	if err := os.MkdirAll(w.watchDir, 0755); err != nil {
		return fmt.Errorf("create watch dir %s: %w", w.watchDir, err)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create fsnotify watcher: %w", err)
	}
	defer fsw.Close()

	if err := fsw.Add(w.watchDir); err != nil {
		return fmt.Errorf("watch dir %s: %w", w.watchDir, err)
	}

	// debounce tracks files waiting to be processed.
	// key = absolute path, value = timer
	debounce := make(map[string]*time.Timer)

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
				map[string]string{"watch_dir": w.watchDir},
			)

		case event, ok := <-fsw.Events:
			if !ok {
				return nil
			}

			// Only care about new files being written or moved in.
			if !isCreateOrWrite(event.Op) {
				continue
			}

			absPath := filepath.Clean(event.Name)

			// Skip directories and hidden files.
			if isDir(absPath) || isHidden(absPath) {
				continue
			}

			// Debounce — cancel existing timer and reset.
			if t, exists := debounce[absPath]; exists {
				t.Stop()
			}

			debounce[absPath] = time.AfterFunc(debounceDelay, func() {
				delete(debounce, absPath)
				w.handleFile(absPath)
			})
		}
	}
}

// WatchDir returns the directory being watched.
func (w *Watcher) WatchDir() string {
	return w.watchDir
}

// ── FILE HANDLER ─────────────────────────────────────────────────────────────

// handleFile validates a file and publishes a drop event.
func (w *Watcher) handleFile(absPath string) {
	info, err := os.Stat(absPath)
	if err != nil {
		// File may have been moved already — not an error.
		return
	}

	// Skip directories that somehow slipped through.
	if info.IsDir() {
		return
	}

	// Skip oversized files.
	if info.Size() > maxFileSizeBytes {
		_ = w.events.SystemAlert("warn",
			fmt.Sprintf("drop: skipping oversized file %s (%d bytes)", info.Name(), info.Size()),
			map[string]string{"path": absPath},
		)
		return
	}

	// Publish drop event — intelligence pipeline picks this up.
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
