# ADR-002 — Workspace Observation Ownership

Date: 2026-03-15
Status: Accepted

---

## Context

Multiple platform components require awareness of filesystem changes
within the workspace. Atlas needs to update its index when files are
created or modified. Forge may trigger workflows on workspace events.
Diagnostic systems may monitor project activity.

Running independent watchers in each service duplicates infrastructure,
increases system load, and creates race conditions when multiple watchers
react to the same event simultaneously.

Nexus already runs a filesystem watcher (`internal/watcher/watcher.go`)
as part of the Drop Intelligence pipeline.

## Decision

Nexus owns filesystem observation for the entire platform.

Nexus extends its existing watcher to publish workspace change events
through the platform event bus alongside existing service events.

## Workspace Event Topics

Workspace event topics are declared as constants in:

    internal/eventbus/bus.go

alongside all existing Nexus event topic constants.

Topic naming convention — identical to existing topics:
- lowercase
- dot-separated
- declared as named constants
- documented with inline comments

Topics to add:

    TopicFileCreated       Topic = "workspace.file.created"
    TopicFileModified      Topic = "workspace.file.modified"
    TopicFileDeleted       Topic = "workspace.file.deleted"
    TopicWorkspaceUpdated  Topic = "workspace.updated"
    TopicProjectDetected   Topic = "workspace.project.detected"

## Consumer Rule

All consumers (Atlas, Forge, and any future services) must:
- import topic constants from the Nexus eventbus package
- never redefine topic strings locally in their own packages

This preserves the single-source guarantee for event topic names.
If a consumer redefines a topic string locally, it silently decouples
from the canonical name and misses events without any compile-time error.

## Implications

- Atlas subscribes to workspace topics to trigger index updates.
- Forge subscribes to workspace topics to trigger event-driven automation
  (Phase 3 of Forge evolution).
- No other component runs a filesystem watcher.
- The Nexus watcher configuration determines which directories are observed.

## Alternatives Considered

**Shared infrastructure watcher component** — rejected because it requires
a new binary and introduces a new failure point for a capability Nexus
already provides.

**Each service runs its own watcher** — rejected because it duplicates
kernel-level inotify resources and creates race conditions between
concurrent handlers for the same filesystem event.

## Consequences

Nexus becomes responsible for the reliability of workspace event delivery.
Atlas and Forge are event consumers only — they never observe the filesystem
directly.
