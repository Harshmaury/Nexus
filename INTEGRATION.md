# INTEGRATION.md
# How to integrate any project with Nexus
# @version: 1.0.0
# @updated: 2026-03-15

---

## Prerequisites

- `engxd` running: `engxd &` or built and in `~/bin/engxd`
- `engx` on `$PATH`: `~/bin/engx`
- For K8s services: `kubectl` in `~/bin/kubectl`, Minikube running
- For remote agents: `engxa` built and deployed on the target machine

---

## Quick Start (5 minutes)

```bash
# 1. Add .nexus.yaml to your project root
# 2. Register
engx register ~/workspace/projects/apps/my-project

# 3. Start
engx project start my-project

# 4. Watch
engx watch
```

---

## .nexus.yaml Reference

```yaml
# ── Required ──────────────────────────────────────────────────
name: my-project        # human-readable name
type: web-api           # web-api | microservice | cli | daemon | library

# ── Recommended ───────────────────────────────────────────────
id: my-project          # stable ID used in all CLI commands (default: lowercase name)
language: go            # go | python | dotnet | node | rust | java
version: 1.0.0

# ── Drop Intelligence ─────────────────────────────────────────
# keywords the detector uses to identify files belonging to this project
keywords:
  - my-project          # project name always first
  - package main        # language-specific identifiers
  - MyProjectConfig     # unique type names, package names, imports

# ── Services ──────────────────────────────────────────────────
services:
  - id: api             # stable service ID — used in logs and state
    name: API Server    # human-readable
    provider: process   # process | docker | k8s
    depends_on: [db]    # list of service IDs that must be running first
    config: |
      {
        "command": "go",
        "args":    ["run", "./cmd/server/"],
        "dir":     "~/workspace/projects/apps/my-project",
        "env":     ["PORT=8090", "ENV=development"],
        "log":     "~/.nexus/logs/my-project-api.log"
      }

  - id: db
    name: PostgreSQL
    provider: docker
    config: |
      {
        "image":   "postgres:16-alpine",
        "ports":   ["5432:5432"],
        "env":     ["POSTGRES_PASSWORD=dev"],
        "volumes": ["my-project-pgdata:/var/lib/postgresql/data"],
        "network": "nexus-net"
      }
```

---

## Provider Config Reference

### process

Runs any local binary. Nexus manages the PID, log file, and clean shutdown.

```json
{
  "command": "python",
  "args":    ["-m", "uvicorn", "main:app", "--port", "8000"],
  "dir":     "~/workspace/projects/apps/my-api",
  "env":     ["PYTHONPATH=.", "DEBUG=1"],
  "log":     "~/.nexus/logs/my-api.log"
}
```

| Field     | Required | Notes                                      |
|-----------|----------|--------------------------------------------|
| `command` | yes      | binary name or absolute path               |
| `args`    | no       | passed verbatim                            |
| `dir`     | no       | working directory; `~` is expanded         |
| `env`     | no       | appended to inherited environment          |
| `log`     | no       | default: `~/.nexus/logs/<service-id>.log`  |

Shutdown: `SIGTERM` → 10s grace → `SIGKILL` to the entire process group.

---

### docker

Runs a Docker container. Nexus owns the full lifecycle — pull, create, start, stop, remove.

```json
{
  "image":   "redis:7-alpine",
  "ports":   ["6379:6379"],
  "env":     ["REDIS_PASSWORD=dev"],
  "volumes": ["my-project-redis:/data"],
  "network": "nexus-net"
}
```

| Field     | Required | Notes                                      |
|-----------|----------|--------------------------------------------|
| `image`   | yes      | pulled automatically if not present        |
| `ports`   | no       | `"host:container"` format                  |
| `env`     | no       | `KEY=VALUE` format                         |
| `volumes` | no       | named volumes or host paths                |
| `network` | no       | default: `nexus-net` (created if absent)   |

Containers are labelled `nexus.managed=true` and `nexus.service=<id>`.
On stop, the container is removed. Data in named volumes is preserved.

---

### k8s

Manages a Kubernetes Deployment via `kubectl`. Uses scale-to-0 as the stop strategy — the Deployment is never deleted.

```json
{
  "namespace":  "default",
  "deployment": "identity-api",
  "replicas":   2,
  "kubeconfig": "~/.kube/config"
}
```

| Field        | Required | Notes                                   |
|--------------|----------|-----------------------------------------|
| `deployment` | yes      | Deployment name in Kubernetes           |
| `namespace`  | no       | default: `default`                      |
| `replicas`   | no       | target replica count on start; default 1|
| `kubeconfig` | no       | default: `~/.kube/config`               |

---

## Service Dependencies

```yaml
services:
  - id: worker
    depends_on: [api, db]   # starts after both api and db are running

  - id: api
    depends_on: [db]        # starts after db

  - id: db                  # no dependencies — starts first
```

Start order resolved automatically via topological sort.
Stop order is the reverse.
If a dependency is not yet running, the dependent is deferred (retried every 5s).
Circular dependencies are detected and logged — affected services are skipped.

---

## Remote Agent Deployment

To run services on another machine, add `agent_id` to the service config:

```json
{
  "image":    "postgres:16-alpine",
  "agent_id": "db-server-1"
}
```

On the remote machine, install and start `engxa`:

```bash
# Build on remote (or copy the binary)
go build -o ~/bin/engxa ./cmd/engxa/

# Run
engxa \
  --server http://192.168.1.10:8080 \
  --token  <shared-secret> \
  --id     db-server-1
```

The agent syncs desired state every 30s and reports actual state back to the
central engxd. The central store remains the single source of truth.

Check agent status from any machine:
```bash
engx agents
```

---

## Drop Intelligence — File Naming Convention

Files routed manually via the drop folder follow this convention for maximum
detection confidence:

```
[project-id]__[feature]__[YYYYMMDD]_[HHMM].[ext]

nexus__engine-reconciler__20260315_0900.go   → confidence 0.50+ (layer 1 match)
ums__identity-service__20260314_1400.cs      → confidence 0.50+
```

Files without this prefix fall through to layers 2–5.
After routing 20+ files, run `engx drop train` to activate the ML layer.

---

## Health and Recovery

Nexus monitors every running service and applies automatic recovery:

| Fail count | Action                     | Delay  |
|------------|----------------------------|--------|
| 1          | restart with back-off      | 5s     |
| 2          | restart with back-off      | 15s    |
| 3          | restart with back-off      | 30s    |
| 4+         | maintenance mode           | manual |

A service in maintenance mode will not be restarted automatically.
To bring it back:
```bash
engx project stop  my-project
engx project start my-project
```

Three or more crashes within 60 minutes → maintenance mode.
All thresholds are configurable in `internal/config/policy.go`.

---

## Observability

```bash
# Live terminal dashboard
engx watch

# JSON metrics snapshot
curl http://127.0.0.1:8080/metrics

# Recent events
engx events
engx events -n 50

# All services and current state
engx services
```

---

## Environment Variables

Override any default without rebuilding:

| Variable                 | Default             | Purpose                      |
|--------------------------|---------------------|------------------------------|
| `NEXUS_DB_PATH`          | `~/.nexus/nexus.db` | SQLite state file location   |
| `NEXUS_SOCKET`           | `/tmp/engx.sock`    | Unix socket path             |
| `NEXUS_HTTP_ADDR`        | `:8080`             | HTTP API bind address        |
| `NEXUS_RECONCILE_INTERVAL` | `5s`              | Reconciler tick rate         |
| `NEXUS_HEALTH_INTERVAL`  | `10s`               | Health poll interval         |
| `NEXUS_HEALTH_TIMEOUT`   | `5s`                | Per-check deadline           |
| `NEXUS_SERVER`           | —                   | engxa: central server URL    |
| `NEXUS_TOKEN`            | —                   | engxa: auth token            |
| `NEXUS_AGENT_ID`         | OS hostname         | engxa: stable agent ID       |

---

## Build Reference

```bash
# Central daemon
go build -o ~/bin/engxd ./cmd/engxd/

# CLI client
go build -o ~/bin/engx ./cmd/engx/

# Remote agent
go build -o ~/bin/engxa ./cmd/engxa/

# All at once
go build -o ~/bin/engxd ./cmd/engxd/ && \
go build -o ~/bin/engx  ./cmd/engx/  && \
go build -o ~/bin/engxa ./cmd/engxa/
```

---

## What Nexus Does Not Do

- It does not know what your project does or how it works internally.
- It does not manage source code, git, or CI/CD pipelines.
- It does not replace Docker Compose or Kubernetes for production.
- It does not provide secrets management.
- It is a local development control plane — not a production orchestrator.
