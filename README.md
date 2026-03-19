# engx

**A local runtime platform for multi-service development.**

Run your services. See what they're doing. Trust what they tell you.

---

## What this is

`engx` is a runtime platform that runs on your machine and manages your local development services — starting them, monitoring them, recovering them when they fail, and giving you a clear picture of what's happening across all of them at once.

It is not a CI/CD pipeline. It is not a container orchestrator. It is not a deployment tool.  
It is the control plane for your local system, the thing that replaces the tab soup of terminals, log files, and manual restarts.

---

## Who it's for

Engineers who work on systems with more than one moving part — microservices, background workers, APIs, databases — and are tired of managing them by hand.

If you've ever had to ask "which service is down?" or "did that restart actually work?" or "which deploy broke this?" — `engx` answers those questions automatically.

---

## Install

```bash
curl -fsSL https://get.engx.dev/install.sh | bash
```

Or via Homebrew:

```bash
brew install harshmaury/engx/engx
```

The installer places three binaries in `~/bin/`:

| Binary | Role |
|--------|------|
| `engx` | CLI — everything you interact with directly |
| `engxd` | Daemon — runs in the background, manages your services |
| `engxa` | Agent — connects remote machines to the same platform |

---

## Quick start

```bash
# 1. Register engxd as a system service (starts automatically at login)
engx platform install

# 2. Register your project
engx init

# 3. Check that everything is healthy
engx doctor
```

**`engx platform install`** — registers the platform daemon with your system's service manager (launchd on macOS, systemd on Linux). After this, `engxd` starts at login and restarts automatically if it crashes. You never manage it manually again.

**`engx init`** — detects your project's language and structure, generates a `.nexus.yaml` descriptor, and registers the project with the platform. It detects Go, Node, Python, Rust, and .NET projects automatically.

**`engx doctor`** — checks the health of every running service, the state of your platform daemon, local environment (ports, permissions, database integrity, binary versions), and reports exactly what's wrong and how to fix it.

---

## Core capabilities

### Run your services

```bash
engx run myservice             # start a service and wait until it's healthy
engx platform start            # start all registered services
engx status                    # show what's running and what isn't
```

Services start in the right order, restart on failure with backoff, and move to a bounded maintenance state if they crash repeatedly. No infinite restart loops.

### See what's happening

```bash
engx logs myservice --follow   # tail live logs for any service
engx status                    # current state of all services at a glance
engx stream                    # live event stream from the platform
```

Every state change, start, stop, crash, and recovery is an event. You can see the full history or watch it live.

### Understand failures

```bash
engx doctor                    # full diagnostic — health, config, environment
engx guard                     # policy findings: repeated failures, unverified services
engx sentinel                  # AI-assisted analysis: what's wrong and why
```

When something breaks, `engx doctor` tells you which service, what state it's in, and what to do next. `engx guard` surfaces patterns — a service failing at a high rate, a project that's never built successfully. `engx sentinel` goes further, correlating events to find root causes: "a deploy of service X happened 3 minutes before this crash cluster."

### Recover automatically

The platform has a three-layer recovery model:

1. **Automatic restart** — crash → backoff (5s, 15s, 30s) → retry
2. **Bounded recovery** — if a service crashes repeatedly, it enters maintenance and the platform attempts a controlled reset (maximum 3 times per hour)
3. **Escalation** — if recovery fails, the platform surfaces a clear alert and stops trying. You are told exactly what happened

There are no silent failures. Every recovery attempt is logged. Every escalation is visible.

### Run builds and workflows

```bash
engx build myservice --path ./myservice    # build via the execution engine
engx exec deploy-pipeline                  # run a named workflow
engx run myservice --wait                  # start and wait until healthy
```

---

## Advanced usage

The following capabilities are available but not required for everyday use. They are built on the same system — no separate config, no separate daemon.

### Distributed tracing

Every operation across the platform carries a trace ID. `engx logs` and `engx sentinel` correlate events by trace, so you can follow a single request or deployment action across every service it touched.

### Automation

```bash
engx on service.crashed deploy-rollback    # trigger a workflow on an event
engx trigger list                          # show active triggers
engx workflow list                         # show defined workflows
```

Triggers fire workflows on platform events. Cron schedules are also supported (`@every 30m`, `@hourly`, `@daily`). All triggered executions appear in history with a `requesting_agent: scheduler` label so you can distinguish them from manual runs.

### Scheduled execution

```bash
# Register a cron trigger
engx on "@every 1h" health-check-workflow
```

Cron triggers share the same execution engine as event-driven triggers — no separate scheduler, no separate queue.

### System introspection

```bash
engx sentinel                              # structured insights + AI analysis
engx guard                                 # policy findings across all services
engx stream                                # raw event stream
```

`engx sentinel` runs correlation rules against the full platform state (crash clusters, deploy correlations, dependency risk, stale projects) and optionally produces an AI narrative when you ask for an explanation.

### Self-upgrade

```bash
engx upgrade               # fetch latest stable release, verify checksum, swap binaries
engx upgrade --dry-run     # show every step without writing anything
engx upgrade --channel beta
```

The upgrade command downloads, verifies SHA256, runs preflight checks against the live daemon, then swaps binaries atomically. If preflight fails, nothing is swapped.

---

## Reliability

These are properties the platform guarantees, not aspirational goals.

**No silent failures.** Every non-success condition is logged with a warning. Services that stop polling upstream do not do so silently — they log what failed and continue serving stale data.

**Bounded recovery.** Automatic recovery attempts are capped (3 per service per hour). When the cap is reached, the platform escalates and stops — it does not loop indefinitely or consume resources retrying a broken service.

**Deterministic state.** The platform's view of your system is always consistent with what's in the database. State is not held in memory without persistence. Restarts are clean.

**Visible escalation.** When automatic recovery fails, you get a specific, named alert — not a generic "something went wrong." The alert names the service, the recovery history, and the suggested action.

**Integrity checks at startup.** The daemon verifies database integrity, checks file permissions, and refuses to start on corruption rather than running with bad state.

---

## Design philosophy

**Local-first.** Everything runs on `127.0.0.1`. No cloud dependency, no external API calls during normal operation. Your data doesn't leave your machine.

**One system, not a collection of tools.** `engx` is a single platform. There is no "plug in your own logger" or "bring your own orchestrator." The tradeoff is intentional — coherence over flexibility.

**Simple surface, powerful core.** The commands you use every day (`status`, `logs`, `doctor`) are short. The full capabilities (tracing, automation, AI analysis) are there when you need them, behind the same CLI.

**No hidden state.** Everything the platform knows is visible. Events, state transitions, recovery history, insights — all accessible via `engx` commands. Nothing happens in the background that you can't observe.

---

## Requirements

- macOS 12+ or Linux (Ubuntu 20.04+, Debian 11+)
- `~/bin/` in your `PATH` (the installer adds this automatically)
- No Docker required for basic usage

Windows is not supported. Use WSL2 on Windows.

---

## Source

- Platform: [github.com/Harshmaury/Nexus](https://github.com/Harshmaury/Nexus)
- Homebrew tap: [github.com/Harshmaury/homebrew-engx](https://github.com/Harshmaury/homebrew-engx)
