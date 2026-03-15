# Architecture Evolution Rules

Version: 1.0.0
Updated: 2026-03-15

---

## Purpose

This document defines the rules that govern how the developer platform
evolves over time. It applies to every change that affects platform
structure, capability boundaries, service interfaces, or communication
patterns.

Following these rules ensures the platform remains modular, replaceable,
and aligned with the capability-based architecture model as it grows.

---

## Rule 1 — Every New Capability Requires an ADR

Before any new platform capability is implemented, an Architecture
Decision Record must be committed to:

    architecture/decisions/

The ADR must state:
- The capability being introduced
- Which domain owns it (Control, Knowledge, or Execution)
- Why it belongs to that domain
- What alternatives were considered and rejected
- What existing capabilities it interacts with

Implementation begins only after the ADR is committed.

---

## Rule 2 — New Services Declare Boundaries Before Code

A new platform service must define its capability boundaries before
any implementation begins.

Required declarations:
- Capability domain (Control / Knowledge / Execution)
- What it owns
- What it explicitly does not own
- Port assignment (following ADR-003 pattern)
- Communication protocol (HTTP/JSON per ADR-003)

These declarations are recorded in the service's architecture
specification document and in the capability boundaries matrix.

---

## Rule 3 — No Cross-Service Internal Imports

Platform services must not import internal packages from other services.

Correct integration:
- HTTP API calls to defined endpoints
- Event bus subscriptions to declared topics

Prohibited:
- Importing `internal/state` from Atlas or Forge
- Importing `internal/eventbus` directly and publishing events
  (only Nexus publishes — other services subscribe)
- Shared in-process data structures between services

Exception: Atlas and Forge may import the Nexus eventbus package
to access topic constant definitions. They may not call Publish
on the bus — only subscribe.

---

## Rule 4 — Event Topics Are Declared in One Place

All platform event topic constants are declared in:

    internal/eventbus/bus.go

Any service that needs a new event topic requests that it be added
to that file. Topics are never declared locally in consuming services.

A new topic requires a comment explaining:
- What event it represents
- Who publishes it
- Who consumes it

---

## Rule 5 — Capability Duplication Is a Design Failure

If a proposed feature duplicates a capability already owned by
another domain, the proposal must be revised before implementation.

Detection approach:
- Check the capability ownership matrix in platform-capability-boundaries.md
- Ask: does this capability require state that another service already owns?
- Ask: does this service need to watch the filesystem, own a project list,
  or manage service runtime? Those belong to Nexus.

When duplication is identified, the options are:
1. Place the capability in the domain that already owns it
2. Redesign the feature so it consumes existing capabilities via API
3. Explicitly transfer ownership and update all ADRs — only if justified

---

## Rule 6 — Architectural Decisions Are Recorded as ADRs

Every significant architectural decision must be recorded.

Significant decisions include:
- New platform services
- New capability domains
- Changes to service communication patterns
- Changes to port assignments
- Changes to event topic ownership
- New runtime providers
- Changes to the intent model

ADR format:

    # ADR-NNN — Title

    Date: YYYY-MM-DD
    Status: Proposed | Accepted | Superseded

    ## Context
    ## Decision
    ## Implications
    ## Alternatives Considered
    ## Consequences

ADRs are append-only. Superseded ADRs are not deleted — they are
updated with a "Superseded by ADR-NNN" note.

---

## Rule 7 — Services Evolve in Phases

New services are implemented in sequenced phases, not shipped complete.

Each phase must:
- Deliver a working, testable capability
- Not require Phase N+1 to be useful
- Be documented in the service architecture specification

Phases are declared before implementation begins. Adding a new phase
to an existing service requires an ADR if the phase introduces
new capability boundaries or integration patterns.

---

## Rule 8 — Interface Stability Is a Contract

Once a service exposes an HTTP endpoint or event topic, that interface
is a contract.

Breaking changes require:
- An ADR documenting the change
- A version bump in the affected service
- A migration path for existing consumers

Non-breaking additions (new endpoints, new optional fields) do not
require an ADR but must be documented in the service specification.

---

## Rule 9 — AI Context Documents Stay Current

Architecture documents used by AI systems must reflect the actual
implemented state of the platform.

Documents in `architecture/` and `AI_CONTEXT.md/` are updated when:
- A new service is added
- A capability moves between domains
- A port assignment changes
- An ADR is accepted that affects the described architecture

Stale AI context produces incorrect reasoning. Keeping these documents
current is as important as keeping code correct.

---

## Rule 10 — The Capability Triangle Is the Stable Foundation

Control / Knowledge / Execution is the permanent architecture foundation.

New capability domains are not added without extraordinary justification.
The vast majority of future platform growth fits within the three
existing domains.

If a proposed service genuinely cannot belong to Control, Knowledge,
or Execution, its design must be reviewed before any ADR is written.
The review question is: is this a new domain or a capability that
belongs in an existing domain but was not anticipated?

In practice, almost every developer platform capability belongs to
one of the three domains. The triangle does not need a fourth side.

---

## ADR Numbering Convention

ADRs are numbered sequentially. The number is permanent.

    ADR-001   Project Registry Authority
    ADR-002   Workspace Observation Ownership
    ADR-003   Service Communication Protocol
    ADR-004   Forge Intent Model
    ADR-005   (next decision)

Gaps in numbering are not allowed. If a proposed ADR is withdrawn,
its number is marked "Withdrawn" and not reused.
