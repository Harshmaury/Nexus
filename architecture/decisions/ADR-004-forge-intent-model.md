# ADR-004 — Forge Intent Model

Date: 2026-03-15
Status: Accepted

---

## Context

Forge is the execution domain of the developer platform. It must accept
developer intent and translate it into coordinated actions across the
platform. Before Forge can be implemented, the primary abstraction for
representing intent must be decided.

Three models were considered:

- Command model — imperative, immediate execution
- Workflow model — declarative, named sequences, reusable
- Automation model — event-driven, reactive triggers

These are not equivalent. The choice determines the input schema,
the execution engine design, and the storage requirements for Phase 1.

## Decision

Forge begins with an imperative command model as its core abstraction.

A command is a structured object representing a single, immediate
action requested by a developer, the CLI, or an automation system.

## Command Object Schema (Phase 1)

Every command must carry these fields from Phase 1 onward:

    {
      "id":         "<uuid>",
      "intent":     "<action name>",
      "target":     "<project or service id>",
      "parameters": { <key-value pairs> },
      "context":    { <ambient metadata> }
    }

Field definitions:

- `id` — unique identifier for this command instance, used for
  tracing, idempotency checks, and workflow composition references
- `intent` — the action to perform (e.g. "build", "deploy", "test")
- `target` — the project or service the action applies to
- `parameters` — action-specific inputs
- `context` — ambient information from the platform at time of
  submission (current branch, workspace root, requesting agent, etc.)

## Why These Fields Are Required in Phase 1

A workflow definition (Phase 2) is structurally a named sequence
of commands. If Phase 1 commands lack `id`, they cannot be referenced
by a workflow step. If they lack `context`, workflows cannot capture
ambient state at submission time.

Designing Phase 1 commands without these fields would require a
breaking schema change at Phase 2. With them, Phase 2 evolution
is additive — a workflow record wraps an ordered list of command
objects with a name and trigger definition.

## Evolution Path

    Phase 1 — Command execution
              Single commands submitted via CLI or API.
              Forge validates, resolves context, executes, reports result.

    Phase 2 — Workflow definitions
              Named sequences of commands stored and reusable.
              Triggered manually or by platform events.
              Workflow definitions reference command schemas by intent.

    Phase 3 — Automation triggers
              Event-driven execution.
              Workspace events (from ADR-002) trigger workflow execution.
              Forge subscribes to Nexus event bus topics.

## What Forge Must Not Do

- Maintain runtime service state (owned by Nexus)
- Replace Nexus orchestration of service lifecycle
- Duplicate Atlas knowledge capabilities
- Accept free-form natural language as its primary input format

Natural language may be translated to a command object by an AI layer
upstream of Forge, but Forge's execution engine always operates on
structured command objects.

## Interaction with Other Services

- Forge queries Nexus (via HTTP, ADR-003) for project and service state
  before executing commands that affect running services.
- Forge queries Atlas (via HTTP, ADR-003) for workspace context to
  populate the `context` field when it cannot be supplied by the caller.
- Forge publishes execution results as events through Nexus (mechanism
  to be defined in a subsequent ADR when Phase 2 begins).

## Alternatives Considered

**Workflow model first** — rejected because it requires a definition
storage layer and parser before any execution can be tested.

**Automation model first** — rejected because it overlaps with Nexus
reconciliation and adds event subscription complexity before the
command layer exists to execute the triggered actions.

## Consequences

All Forge Phase 1 implementation assumes the five-field command object
as the input unit. Any CLI command, API endpoint, or AI-generated
request that reaches Forge must be translated into this schema before
execution begins. The translation layer is the boundary between
Forge's interface and its execution engine.
