// @nexus-project: nexus
// @nexus-path: pkg/events/topics.go
// Package events exposes platform event topic constants for cross-module
// consumers such as Atlas and Forge.
//
// Go's module system prohibits importing internal/ packages from external
// modules. This package re-exports the workspace topic constants and payload
// types that Atlas and Forge need to subscribe to workspace events (ADR-002).
//
// IMPORT RULE:
//   External consumers (Atlas, Forge) import:
//     "github.com/Harshmaury/Nexus/pkg/events"
//
//   Internal Nexus components import:
//     "github.com/Harshmaury/Nexus/internal/eventbus"
//
// PUBLISH RULE:
//   Only Nexus internal components call bus.Publish.
//   External consumers call bus.Subscribe via their own event loop
//   (see Atlas internal/nexus/subscriber.go for the polling pattern).
package events

import "time"

// Topic is the type for all platform event topic names.
// Mirrors eventbus.Topic for cross-module use.
type Topic = string

// ── Workspace topics (ADR-002) ────────────────────────────────────────────
// Published by: internal/watcher/watcher.go
// Consumers:    Atlas (index updates), Forge Phase 3 (automation triggers)
//
// RULE: import these constants — never redefine topic strings locally.

const (
	// TopicWorkspaceFileCreated is published when a new file appears
	// in the watched workspace directories.
	TopicWorkspaceFileCreated = "workspace.file.created"

	// TopicWorkspaceFileModified is published when an existing workspace
	// file is written to.
	TopicWorkspaceFileModified = "workspace.file.modified"

	// TopicWorkspaceFileDeleted is published when a workspace file
	// is removed or renamed.
	TopicWorkspaceFileDeleted = "workspace.file.deleted"

	// TopicWorkspaceUpdated is published after a batch of file events
	// settles (debounce window). Signals a logical workspace change.
	TopicWorkspaceUpdated = "workspace.updated"

	// TopicWorkspaceProjectDetected is published when the watcher
	// finds a new project manifest in the workspace.
	TopicWorkspaceProjectDetected = "workspace.project.detected"
)

// ── Payload types ─────────────────────────────────────────────────────────
// These mirror the payload types declared in internal/eventbus/bus.go.
// Kept in sync manually — if eventbus payloads change, update these too.

// WorkspaceFilePayload is the payload for file created/modified/deleted events.
type WorkspaceFilePayload struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Extension string    `json:"extension"`
	SizeBytes int64     `json:"size_bytes"`
	EventAt   time.Time `json:"event_at"`
}

// WorkspaceUpdatedPayload is the payload for the workspace.updated batch event.
type WorkspaceUpdatedPayload struct {
	WatchDir string    `json:"watch_dir"`
	EventAt  time.Time `json:"event_at"`
}

// WorkspaceProjectPayload is the payload for workspace.project.detected.
type WorkspaceProjectPayload struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	DetectedBy string    `json:"detected_by"`
	DetectedAt time.Time `json:"detected_at"`
}
