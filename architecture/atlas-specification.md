# Atlas Architecture Specification

Version: 1.0.0
Updated: 2026-03-15
Domain: Knowledge
Port: 127.0.0.1:8081

---

## Purpose

Atlas is the knowledge domain of the developer platform. It provides
structured awareness of the workspace so developers, platform services,
and AI systems can understand project structure, relationships, and
architectural state without querying the filesystem directly.

---

## Position in Platform Architecture

    Interface Layer      engx CLI
    Control Plane        Nexus
    Service Layer        Atlas  ←  Workspace Knowledge Service
    Execution Layer      Runtime Providers
    Infrastructure       Filesystem / OS

Atlas is a platform service. It does not orchestrate services and does
not control runtime state.

---

## What Atlas Owns

- Workspace discovery and project detection
- Source file indexing
- Architecture document indexing
- Structured capability claim extraction
- Workspace knowledge graph (Phase 2)
- Architecture conflict detection queries (Phase 2)
- AI context generation

---

## What Atlas Does Not Own

- Project registry authority (Nexus — ADR-001)
- Filesystem watchers (Nexus — ADR-002)
- Service lifecycle or state
- Workflow execution
- Runtime provider management

Atlas never starts, stops, or modifies a service. It reads and indexes.

---

## Implementation Phases

### Phase 1 — Workspace Knowledge Index

Build the foundational index layer. No graph construction.

Capabilities:

**Workspace discovery**
Scan `~/workspace/` for registered projects and workspace components.
Use Nexus project registry (ADR-001) as the authoritative project list.
Supplement with filesystem scanning for unregistered directories.

**Source indexing**
Index source files by: path, language, module, package, entry points.
Store in Atlas internal database (implementation detail, not exposed).
Update on `workspace.file.created` and `workspace.file.modified` events
from the Nexus event bus (ADR-002).

**Architecture document indexing**
Detect and index: architecture constraints, capability definitions,
platform philosophy documents, ADRs, integration guides.
Index by: document type, date, decision status, referenced services.

**Project metadata ingestion**
Ingest project metadata from Nexus API on startup and on
`project.registered` events.
Store derived representations (language, type, path, service count).

**AI context generation**
Produce structured workspace summaries for AI sessions on demand.

    {
      "workspace_root": "/home/harsh/workspace",
      "projects": ["nexus", "atlas"],
      "languages": ["go"],
      "services": ["engxd", "engx"],
      "architecture_docs": ["constraints", "platform-model"]
    }

### Phase 2 — Structured Capability Model

Depends on Phase 1 index being complete.

**Structured capability claims**
Architecture documents must produce structured records, not just text.

Each capability claim extracted from documents takes this form:

    {
      "capability": "project-registry",
      "owner":      "nexus",
      "scope":      "platform",
      "interfaces": ["HTTP /projects", "event: project.registered"],
      "dependencies": []
    }

This is the minimum schema. Without structured claims, conflict
detection produces noise rather than signal.

**Workspace graph**
Nodes: projects, services, modules, documents, datasets
Edges: depends_on, implements, references, contains

Built from: source imports, `.nexus.yaml` declarations, ADR references,
and event relationships.

**Architecture conflict detection**
Queries that surface potential issues:

- Duplicate capability ownership (two components claim the same capability)
- Undefined capability consumers (event consumed but no publisher declared)
- Undeclared dependencies (service calls another without ADR reference)
- Orphaned ADRs (decision references a service that no longer exists)

Results are informational signals. Atlas does not resolve conflicts.

---

## Integration with Nexus

**Project data**
Atlas queries `GET http://127.0.0.1:8080/projects` on startup.
Atlas subscribes to project lifecycle events from the Nexus event bus.

**Workspace events**
Atlas subscribes to workspace event topics declared in Nexus eventbus:
- `workspace.file.created`
- `workspace.file.modified`
- `workspace.file.deleted`
- `workspace.project.detected`

Atlas imports topic constants from `github.com/Harshmaury/Nexus/internal/eventbus`.
Atlas never redefines topic strings locally.

---

## Atlas HTTP API (127.0.0.1:8081)

All responses use the standard platform envelope: `{ok, data, error}`.

Phase 1 endpoints:

    GET  /workspace                workspace summary
    GET  /workspace/projects       list indexed projects
    GET  /workspace/project/:id    project detail with file count and language
    GET  /workspace/search?q=      search indexed source
    GET  /workspace/context        AI-ready workspace context
    GET  /health                   liveness probe

Phase 2 endpoints (added when Phase 2 begins):

    GET  /workspace/graph          full workspace knowledge graph
    GET  /workspace/capabilities   structured capability claim list
    GET  /workspace/conflicts      architecture conflict detection results
    GET  /workspace/architecture   architecture document summary

---

## CLI Integration

Atlas capabilities are exposed through `engx workspace` subcommands
which call the Atlas HTTP API:

    engx workspace                 workspace summary
    engx workspace projects        list discovered projects
    engx workspace info <id>       project detail
    engx workspace search <query>  search workspace
    engx workspace context         AI context snapshot

Phase 2:

    engx workspace architecture           architecture summary
    engx workspace architecture-conflicts conflict detection report

---

## Storage Model

Atlas manages its own internal storage. The implementation is an
internal detail — not exposed to other services.

Guiding constraints:
- Must support full-text search over source and document content
- Must support structured queries over capability claims (Phase 2)
- Must be replaceable without changing Atlas's HTTP API

Other platform components interact with Atlas only through its HTTP API.
No component imports Atlas internal packages.

---

## Environment Variables

    ATLAS_HTTP_ADDR     default :8081
    ATLAS_WORKSPACE     default ~/workspace
    NEXUS_HTTP_ADDR     default 127.0.0.1:8080  (for project queries)

---

## Atlas Design Principles

1. Atlas reads — it never writes to Nexus state or service state.
2. Atlas indexes — it never orchestrates or executes.
3. Atlas serves — it answers queries, it does not push unsolicited data.
4. Atlas defers — project authority stays in Nexus, events come from Nexus.
5. Atlas phases — Phase 2 capabilities require Phase 1 index to exist.
