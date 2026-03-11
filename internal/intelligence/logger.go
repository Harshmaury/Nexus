// @nexus-project: nexus
// @nexus-path: internal/intelligence/logger.go
// DropLogger writes every routing decision to the download_log table.
// It is the audit trail for the Drop Intelligence system —
// every file that passes through the pipeline is recorded here.
package intelligence

import (
	"time"

	"github.com/Harshmaury/Nexus/internal/state"
)

// ── DROP LOG ENTRY ───────────────────────────────────────────────────────────

// DropLogEntry is one record in the download_log table.
type DropLogEntry struct {
	OriginalName string
	RenamedTo    string
	Project      string
	Source       string // always "nexus-drop"
	Destination  string
	Method       string  // detection method(s) used
	Confidence   float64
	Action       string  // moved | prompted | tagged | skipped
	DroppedAt    time.Time
}

// ── DROP LOGGER ───────────────────────────────────────────────────────────────

// DropLogger writes download log entries to the state store.
type DropLogger struct {
	store *state.Store
}

// NewDropLogger creates a DropLogger backed by the state store.
func NewDropLogger(store *state.Store) *DropLogger {
	return &DropLogger{store: store}
}

// Log writes a routing decision to the download_log table.
func (l *DropLogger) Log(entry DropLogEntry) error {
	if entry.Source == "" {
		entry.Source = "nexus-drop"
	}
	if entry.DroppedAt.IsZero() {
		entry.DroppedAt = time.Now().UTC()
	}

	return l.store.LogDownload(&state.DownloadLog{
		OriginalName: entry.OriginalName,
		RenamedTo:    entry.RenamedTo,
		Project:      entry.Project,
		Source:       entry.Source,
		Destination:  entry.Destination,
		Method:       entry.Method,
		Confidence:   entry.Confidence,
	})
}
