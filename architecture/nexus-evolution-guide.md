# Nexus Architecture Evolution Guide

Version: 1.0.0
Updated: 2026-03-15
Domain: Control

---

## Purpose

This document defines how Nexus evolves while remaining the platform
control plane. It states what Nexus owns permanently, what rules govern
adding new capabilities to Nexus, and what must never move out of Nexus.

---

## Control Plane Responsibilities

Nexus owns these capabilities permanently. They do not move to other
services regardless of how the platform grows.

**Project registry**
Nexus is the canonical source of project registration and lifecycle.
See ADR-001.

**Event bus**
Nexus owns the platform event bus (`internal/eventbus`). All event
topic constants are declared here. No other service declares topics.
See ADR-002.

**Filesystem observation**
Nexus runs the workspace watcher and publishes filesystem events.
No other service runs an independent filesystem watcher.
See ADR-002.

**Service state**
Nexus maintains desired and actual state for all managed services.
The SQLite state store is a Nexus internal. No other service writes
to it directly.

**Runtime orchestration**
Nexus reconciles desired state against actual state via the Engine.
The reconciler, health controller, and recovery controller belong to
Nexus permanently.

**Runtime providers**
Docker, Process, and K8s providers belong to Nexus.
New runtime providers (Nomad, Podman, systemd) are added to Nexus,
not to new services.

---

## Event Bus Ownership Rules

The event bus is defined in `internal/eventbus/bus.go`.

Rules that must not be violated:

1. Topic constants are declared only in `internal/eventbus/bus.go`.
2. All services import topic constants from this package.
3. No service redefines a topic string locally.
4. New topics follow the naming convention: lowercase, dot-separated.
5. Topics are grouped by domain with inline documentation.

Current topic domains:
- `service.*` — service lifecycle events
- `workspace.*` — filesystem and workspace events (ADR-002)
- `drop.*` — Drop Intelligence pipeline events
- `system.*` — platform-level alerts

New topic domains added by future services follow the same pattern and
are declared in `internal/eventbus/bus.go` regardless of which service
publishes them.

---

## Filesystem Observation Ownership Rules

Nexus runs one watcher. Extensions follow these rules:

1. New watch targets (directories, patterns) are configured in Nexus.
2. New event types from the watcher are added as topics to the event bus.
3. Atlas and Forge subscribe to events — they never watch directly.
4. The watcher configuration is controlled via environment variables or
   Nexus configuration, not by consuming services.

---

## Integration Rules for New Services

When a new service joins the platform and needs to interact with Nexus:

1. It communicates via the Nexus HTTP API (`127.0.0.1:8080`).
2. It subscribes to relevant event bus topics if it needs real-time
   workspace or service state changes.
3. It does not import Nexus internal packages directly.
4. It does not write to the Nexus SQLite database.
5. It declares its own capability domain and does not duplicate
   Nexus responsibilities.

---

## Service Coordination Boundaries

Nexus coordinates service lifecycle. Other services request coordination
through the Nexus API.

What Nexus does:
- Accepts start/stop/register commands
- Reconciles service state via providers
- Publishes state change events

What Nexus does not do:
- Execute developer workflows (Forge)
- Index workspace source code (Atlas)
- Perform architecture analysis (Atlas)
- Plan multi-step automation sequences (Forge)

If a proposed Nexus feature belongs to one of those categories, it
belongs in Atlas or Forge instead.

---

## What Must Never Leave Nexus

These responsibilities must not be extracted into separate services:

- Project registry authority
- Service state store (SQLite)
- Event bus topic declarations
- Reconciler engine
- Health and recovery controllers
- Runtime provider implementations
- Filesystem watcher

Extracting any of these creates a distributed coordination problem
that the platform architecture is explicitly designed to avoid.

---

## Adding New Capabilities to Nexus

A new capability belongs in Nexus if and only if it meets one of these
criteria:

1. It requires write access to service state.
2. It produces events that other services depend on.
3. It is a runtime provider for a new execution environment.
4. It is a new controller that enforces platform-level policy.

Capabilities that do not meet these criteria belong in Atlas, Forge,
or a future service in the appropriate capability domain.

All new Nexus capabilities must be accompanied by an ADR.
