# MAIN_GO_PATCH.md — Nexus Phase 21

Add `upgradeCmd` to `rootCmd()` in `cmd/engx/main.go`.

Find the `root.AddCommand(` block and add one line:

```go
root.AddCommand(
    projectCmd(&socketPath),
    registerCmd(&socketPath, &httpAddr),
    servicesCmd(&socketPath, &httpAddr),
    eventsCmd(&socketPath),
    dropCmd(&socketPath),
    watchCmd(&socketPath),
    agentsCmd(&httpAddr),
    platformCmd(&socketPath, &httpAddr),
    doctorCmd(&httpAddr),
    logsFollowCmd(),
    buildCmd(&httpAddr),
    checkCmd(&httpAddr),
    runCmd(&socketPath, &httpAddr),
    initCmd(&socketPath, &httpAddr),
    traceCmd(),
    versionCmd(),
    statusCmd(&httpAddr),
    sentinelCmd(&httpAddr),
    workflowCmd(&httpAddr),
    triggerCmd(&httpAddr),
    guardCmd(&httpAddr),
    onCmd(&httpAddr),
    execCmd(&httpAddr),
    ciCmd(&httpAddr),
    eventsStreamCmd(&httpAddr),
    upgradeCmd(&httpAddr),   // ← ADD THIS LINE (phase 21 / ADR-028)
)
```

No other changes to main.go are required.
