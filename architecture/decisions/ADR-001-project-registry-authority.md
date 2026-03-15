# ADR-001 — Project Registry Authority

Date: 2026-03-15
Status: Accepted

---

## Context

The developer platform requires a canonical source of project registration
and lifecycle information. Multiple components — including Atlas and Forge —
need access to project metadata. Without a clear authority, duplicate
registries introduce conflicting sources of truth and inconsistent state.

## Decision

Nexus is the authoritative project registry.

All project registration, update, and lifecycle transitions originate
from Nexus and are persisted in the Nexus state store.

## Implications

- Atlas reads project information by querying the Nexus HTTP API or
  subscribing to project lifecycle events published by Nexus.
- Forge queries Nexus for project lifecycle state before executing
  workflows that target a specific project.
- No other platform component maintains a canonical project list.
- If a component requires derived project data (indexes, graphs, summaries),
  it builds that representation on top of Nexus data — it does not
  replicate the source record.

## Alternatives Considered

**Shared project registry service** — rejected because it introduces a
new dependency and coordination point without adding capability.

**Each service maintains its own registry** — rejected because it
produces conflicting state and makes project lifecycle events unreliable.

## Consequences

Project registration flows through Nexus exclusively. New platform
services that need project information must consume it from Nexus,
not reimplement it.
