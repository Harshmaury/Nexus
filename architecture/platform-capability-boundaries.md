# Platform Capability Boundaries

Version: 1.0.0
Updated: 2026-03-15

---

## The Three Capability Domains

The developer platform is organised around three stable capability domains.
These domains are permanent. Implementations within them may change;
the domains themselves do not.

    Control    — Nexus     coordinates the system
    Knowledge  — Atlas     understands the system
    Execution  — Forge     acts on the system

---

## Domain Definitions

### Control Domain — Nexus

The control domain owns everything related to runtime coordination,
service state, and system authority.

Capabilities that belong here:

- Project registration and lifecycle
- Service desired and actual state management
- Runtime reconciliation
- Health monitoring and recovery policy
- Runtime provider management (Docker, Process, K8s)
- Platform event bus and topic ownership
- Filesystem observation and workspace event publication

A capability belongs in the control domain if it requires write access
to service state or produces events that other services depend on.

### Knowledge Domain — Atlas

The knowledge domain owns everything related to understanding the
workspace — its structure, relationships, and architectural state.

Capabilities that belong here:

- Workspace discovery and project detection
- Source file indexing and search
- Architecture document indexing
- Structured capability claim extraction
- Workspace relationship graph
- Architecture conflict detection
- AI context generation

A capability belongs in the knowledge domain if its primary output
is structured information rather than a state change or an action.

### Execution Domain — Forge

The execution domain owns everything related to translating developer
intent into coordinated platform actions.

Capabilities that belong here:

- Command intake, validation, and translation
- Intent execution pipeline
- Workflow definition and orchestration (Phase 2)
- Event-driven automation (Phase 3)

A capability belongs in the execution domain if its primary purpose
is to perform work in response to developer or system intent.

---

## Capability Overlap Rules

These rules prevent responsibilities from drifting across domain boundaries.

**Rule 1 — Single ownership**
Every platform capability has exactly one owning domain. If a capability
could plausibly belong to two domains, it belongs to the domain whose
core purpose most directly requires it.

**Rule 2 — Read access is not ownership**
A service may read information produced by another domain without
claiming ownership of that capability. Atlas reads from Nexus.
Forge reads from Atlas and Nexus. Neither owns what it reads.

**Rule 3 — Events do not transfer ownership**
Publishing an event does not grant the subscriber ownership of the
capability that produced it. Nexus publishes workspace events; Atlas
consumes them. Nexus still owns workspace observation.

**Rule 4 — New capabilities require explicit assignment**
Any new platform capability must be explicitly assigned to a domain
before implementation begins. Unassigned capabilities are not built.
Assignment is recorded in an ADR.

**Rule 5 — Duplication is prohibited**
No two domains implement the same capability. If a proposed feature
for Atlas or Forge replicates a Nexus responsibility, the feature
either belongs in Nexus or is redesigned so that it does not duplicate.

---

## Capability Overlap Matrix

The following matrix records which capabilities each domain
explicitly does and does not own.

| Capability                  | Nexus | Atlas | Forge |
|-----------------------------|-------|-------|-------|
| Project registry            | ✓     | ✗     | ✗     |
| Service state management    | ✓     | ✗     | ✗     |
| Runtime orchestration       | ✓     | ✗     | ✗     |
| Health monitoring           | ✓     | ✗     | ✗     |
| Recovery policy             | ✓     | ✗     | ✗     |
| Runtime providers           | ✓     | ✗     | ✗     |
| Event bus ownership         | ✓     | ✗     | ✗     |
| Filesystem observation      | ✓     | ✗     | ✗     |
| Workspace discovery         | ✗     | ✓     | ✗     |
| Source indexing             | ✗     | ✓     | ✗     |
| Architecture artifact index | ✗     | ✓     | ✗     |
| Capability claim model      | ✗     | ✓     | ✗     |
| Workspace graph             | ✗     | ✓     | ✗     |
| Conflict detection          | ✗     | ✓     | ✗     |
| AI context generation       | ✗     | ✓     | ✗     |
| Command intake              | ✗     | ✗     | ✓     |
| Intent execution            | ✗     | ✗     | ✓     |
| Workflow definitions        | ✗     | ✗     | ✓     |
| Automation triggers         | ✗     | ✗     | ✓     |

---

## Boundary Enforcement Mechanisms

**ADRs** — capability ownership is recorded in architecture decision
records before implementation begins. ADR-001 through ADR-004 establish
the initial boundaries.

**No cross-package imports** — platform services do not import internal
packages from other services. Integration occurs exclusively through
HTTP APIs and event bus subscriptions.

**Event topic ownership** — all event topics are declared in the Nexus
event bus package. A service that needs a new topic requests it be added
there rather than defining it locally.

**API-first design** — capabilities are accessed through defined HTTP
endpoints. Any capability not exposed through an API is not a platform
capability — it is an internal implementation detail.

---

## Adding New Services to the Platform

A new service joins the platform by:

1. Identifying which capability domain it belongs to.
2. Declaring its capabilities explicitly (what it owns).
3. Declaring what it does not own (what remains in other domains).
4. Recording the capability assignment in an ADR.
5. Exposing its capabilities via HTTP API following ADR-003.
6. Consuming cross-service information via API or events only.

A new service that cannot clearly state its capability domain and
boundaries is not ready to be added to the platform.
