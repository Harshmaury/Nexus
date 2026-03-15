# ADR-003 — Service Communication Protocol

Date: 2026-03-15
Status: Accepted

---

## Context

Atlas and Forge are new platform services that must expose interfaces
to the CLI, to each other, and to Nexus. The platform requires a
consistent communication model so services are predictably reachable
and the CLI does not need different protocols for different services.

Nexus already exposes an HTTP/JSON API on `127.0.0.1:8080`. The CLI
and any AI tooling already know how to call this pattern.

## Decision

All platform services communicate through HTTP/JSON APIs on localhost.

Each service runs on its own assigned port and exposes a JSON API
following the same envelope pattern as the existing Nexus API.

## Port Assignments

    Nexus   127.0.0.1:8080
    Atlas   127.0.0.1:8081
    Forge   127.0.0.1:8082

Ports are sequential, easy to remember, and do not conflict with
common development service ports (3000, 5432, 6379, 8000, 9090).

These assignments are fixed. Future services that join the platform
must not use 8080–8082. New port assignments must be recorded in a
subsequent ADR.

## Response Envelope

All services use the same JSON response envelope:

    {
      "ok":    true | false,
      "data":  <payload>,
      "error": "<message if ok=false>"
    }

This consistency allows the CLI and AI tools to handle responses
uniformly regardless of which service produced them.

## Environment Variable Overrides

Each service respects an environment variable to override its default
address:

    NEXUS_HTTP_ADDR   default :8080
    ATLAS_HTTP_ADDR   default :8081
    FORGE_HTTP_ADDR   default :8082

## CLI Communication

The CLI (`engx`) communicates with Nexus via Unix socket for low-latency
local commands and via HTTP for agent and multi-service queries.

Atlas and Forge are reached by the CLI exclusively via HTTP.

The `--http` flag on `engx` may be extended to support targeting a
specific service by name or port.

## Alternatives Considered

**Unix socket per service** — rejected because it requires socket path
management and makes cross-machine and AI tool access more complex.
Nexus retains its Unix socket for backwards compatibility with existing
CLI commands; new services do not need it.

**Control-plane mediated communication** — rejected because it makes
Nexus a bottleneck for all inter-service queries and adds latency to
knowledge and execution operations that do not require coordination.

**gRPC** — rejected at this stage because it requires a protobuf toolchain
and code generation step. HTTP/JSON is sufficient for local service
communication and is consistent with the existing platform.

## Consequences

Every platform service must implement an HTTP server on its assigned port.
Services that need to query each other do so via HTTP. There is no shared
memory, no shared database connection, and no direct function calls
across service boundaries.
