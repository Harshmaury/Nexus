# Forge Architecture Specification

Version: 1.0.0
Updated: 2026-03-15
Domain: Execution
Port: 127.0.0.1:8082

---

## Purpose

Forge is the execution domain of the developer platform. It translates
developer intent into coordinated actions across the platform. It is the
system that acts — where Nexus coordinates and Atlas understands.

---

## Position in Platform Architecture

    Interface Layer      engx CLI
    Control Plane        Nexus
    Service Layer        Atlas, Forge  ←  Workflow Execution Service
    Execution Layer      Runtime Providers (owned by Nexus)
    Infrastructure       Filesystem / OS

Forge is a platform service. It does not maintain runtime service state
and does not replace Nexus orchestration.

---

## What Forge Owns

- Command intake and validation
- Intent resolution and context enrichment
- Execution pipeline coordination
- Workflow definition storage and execution (Phase 2)
- Event-driven automation triggers (Phase 3)

---

## What Forge Does Not Own

- Runtime service state (Nexus)
- Service start/stop decisions (Nexus)
- Project registry (Nexus — ADR-001)
- Filesystem observation (Nexus — ADR-002)
- Workspace knowledge and indexing (Atlas)
- Architecture conflict detection (Atlas)

Forge never starts or stops services directly. It instructs Nexus to
do so via the Nexus HTTP API.

---

## Intent Model (ADR-004)

Forge operates on structured command objects. All inputs — CLI commands,
API requests, AI-generated instructions — are translated into this schema
before execution begins.

### Command Object Schema

    {
      "id":         "<uuid>",
      "intent":     "<action name>",
      "target":     "<project or service id>",
      "parameters": { <key-value pairs> },
      "context":    { <ambient metadata> }
    }

Field definitions:

- `id` — unique command identifier used for tracing and idempotency
- `intent` — the action to perform: "build", "deploy", "test", "run"
- `target` — the project or service the action applies to
- `parameters` — intent-specific inputs (build flags, environment, etc.)
- `context` — ambient platform state at submission time

Context is populated by Forge from Atlas and Nexus if not supplied:

    {
      "workspace_root":  "/home/harsh/workspace",
      "project_path":    "/home/harsh/workspace/projects/apps/nexus",
      "language":        "go",
      "requesting_agent": "cli",
      "timestamp":       "2026-03-15T04:23:00Z"
    }

---

## Implementation Phases

### Phase 1 — Command Execution

Single commands submitted via CLI or HTTP API.

Execution pipeline:

    1. Receive command object
    2. Validate schema (required fields, known intent)
    3. Resolve target (query Nexus for project/service state)
    4. Enrich context (query Atlas for workspace knowledge if needed)
    5. Execute intent (invoke appropriate handler)
    6. Report result

Intent handlers in Phase 1:

- `build` — run build command for target project
- `test` — run test suite for target project
- `run` — start a process (via Nexus provider)
- `deploy` — coordinate deployment sequence

Phase 1 does not persist commands or results beyond the request lifecycle.

### Phase 2 — Workflow Definitions

Named sequences of commands, stored and reusable.

A workflow definition wraps an ordered list of command objects:

    {
      "id":       "<uuid>",
      "name":     "full-deploy",
      "steps":    [<command>, <command>, ...],
      "trigger":  "manual"
    }

Workflow definitions are stored in Forge's internal database.
They are executed by submitting a workflow run request.

Phase 2 is additive — the command schema from Phase 1 is unchanged.

### Phase 3 — Automation Triggers

Event-driven execution. Forge subscribes to Nexus event bus topics
and maps events to workflow definitions.

    {
      "event":    "workspace.file.modified",
      "filter":   { "extension": ".go" },
      "workflow": "run-tests"
    }

Phase 3 uses the workspace event topics defined in ADR-002.

---

## Interaction with Nexus

Forge calls the Nexus HTTP API to:
- Query project and service state before execution
- Request service start/stop as part of intent execution
- Read recent events for context enrichment

Forge does not import Nexus internal packages.
Forge does not write to the Nexus state store.

---

## Interaction with Atlas

Forge calls the Atlas HTTP API to:
- Enrich command context with workspace knowledge
- Resolve project paths and language details for intent handlers
- Retrieve architecture summaries for AI-assisted workflow planning (future)

Forge only reads from Atlas. It does not modify Atlas indexes.

---

## Forge HTTP API (127.0.0.1:8082)

All responses use the standard platform envelope: `{ok, data, error}`.

Phase 1 endpoints:

    POST /commands              submit a command object, returns result
    GET  /commands/:id          retrieve result of a submitted command
    GET  /intents               list supported intent names
    GET  /health                liveness probe

Phase 2 endpoints:

    POST /workflows             create a workflow definition
    GET  /workflows             list stored workflows
    GET  /workflows/:id         retrieve workflow definition
    POST /workflows/:id/run     execute a workflow

Phase 3 endpoints:

    POST /triggers              register an event-to-workflow mapping
    GET  /triggers              list active triggers
    DELETE /triggers/:id        remove a trigger

---

## CLI Integration

Forge capabilities are exposed through `engx` subcommands:

Phase 1:

    engx run <intent> <target> [--param key=value]
    engx build <project>
    engx test <project>

Phase 2:

    engx workflow create <name>
    engx workflow run <name>
    engx workflow list

Phase 3:

    engx trigger add --on <event> --run <workflow>
    engx trigger list

---

## Translation Layer

The boundary between Forge's interface and its execution engine is the
command object translation layer. Every entry point (CLI, HTTP API,
event trigger) passes through this layer before reaching the executor.

This layer is responsible for:
- Parsing the raw input into a command object
- Validating required fields
- Generating a unique `id` if not supplied
- Populating `context` from Atlas and Nexus

The execution engine always operates on validated command objects.
It never accepts free-form strings.

---

## Environment Variables

    FORGE_HTTP_ADDR     default :8082
    NEXUS_HTTP_ADDR     default 127.0.0.1:8080
    ATLAS_HTTP_ADDR     default 127.0.0.1:8081

---

## Forge Design Principles

1. Forge acts — it executes, Atlas reads, Nexus coordinates.
2. Forge translates — all input becomes a command object before execution.
3. Forge delegates — service lifecycle decisions go through Nexus.
4. Forge phases — workflow and automation layers build on the command layer.
5. Forge is stateless in Phase 1 — no persistence until Phase 2 begins.
