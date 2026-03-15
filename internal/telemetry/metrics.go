// @nexus-project: nexus
// @nexus-path: internal/telemetry/metrics.go
// Package telemetry provides platform metrics for the Nexus daemon.
//
// DESIGN — zero new dependencies:
//   Uses sync/atomic for counters and gauges. Metrics are exposed as a
//   snapshot struct that the HTTP API serialises to JSON at GET /metrics.
//   This is intentional — adding Prometheus client_golang or OTel SDK
//   would pull in 30+ transitive deps and require a running collector.
//   For a local developer platform, a JSON endpoint is sufficient and
//   readable in any browser or curl call.
//
//   Upgrade path: when a Prometheus scrape target is needed (e.g. Grafana
//   dashboard for UMS), replace the snapshot serialiser with a Prometheus
//   registry. All counter/gauge increment call sites remain unchanged.
//
// THREAD SAFETY:
//   All operations use atomic loads/stores — safe to call from any goroutine.
//
// METRICS EXPOSED:
//   reconcile_cycles_total     number of reconciler ticks since daemon start
//   services_started_total     cumulative services started by the reconciler
//   services_stopped_total     cumulative services stopped
//   services_crashed_total     cumulative start failures
//   services_deferred_total    cumulative deferred starts (waiting on dep)
//   reconcile_errors_total     cumulative reconcile cycle errors
//   services_running           current count of actually-running services
//   services_in_maintenance    current count in maintenance mode
//   uptime_seconds             seconds since daemon start
package telemetry

import (
	"sync/atomic"
	"time"
)

// ── METRICS ──────────────────────────────────────────────────────────────────

// Metrics holds all platform counters and gauges.
// Create a single instance in engxd and pass it to components that need it.
type Metrics struct {
	// Counters — monotonically increasing
	RecycleCyclesTotal    atomic.Int64
	ServicesStartedTotal  atomic.Int64
	ServicesStoppedTotal  atomic.Int64
	ServicesCrashedTotal  atomic.Int64
	ServicesDeferredTotal atomic.Int64
	ReconcileErrorsTotal  atomic.Int64

	// Gauges — point-in-time values
	ServicesRunning       atomic.Int64
	ServicesInMaintenance atomic.Int64

	startedAt time.Time
}

// New creates and returns a ready-to-use Metrics instance.
func New() *Metrics {
	return &Metrics{startedAt: time.Now().UTC()}
}

// ── COUNTER METHODS ───────────────────────────────────────────────────────────

func (m *Metrics) ReconcileCycle()     { m.RecycleCyclesTotal.Add(1) }
func (m *Metrics) ServiceStarted()     { m.ServicesStartedTotal.Add(1) }
func (m *Metrics) ServiceStopped()     { m.ServicesStoppedTotal.Add(1) }
func (m *Metrics) ServiceCrashed()     { m.ServicesCrashedTotal.Add(1) }
func (m *Metrics) ServiceDeferred()    { m.ServicesDeferredTotal.Add(1) }
func (m *Metrics) ReconcileError()     { m.ReconcileErrorsTotal.Add(1) }

// ── GAUGE METHODS ─────────────────────────────────────────────────────────────

func (m *Metrics) SetRunning(n int64)       { m.ServicesRunning.Store(n) }
func (m *Metrics) SetMaintenance(n int64)   { m.ServicesInMaintenance.Store(n) }

// ── SNAPSHOT ──────────────────────────────────────────────────────────────────

// Snapshot is the JSON-serialisable view of all current metric values.
// Returned by the HTTP GET /metrics endpoint.
type Snapshot struct {
	UptimeSeconds         float64 `json:"uptime_seconds"`
	ReconcileCyclesTotal  int64   `json:"reconcile_cycles_total"`
	ServicesStartedTotal  int64   `json:"services_started_total"`
	ServicesStoppedTotal  int64   `json:"services_stopped_total"`
	ServicesCrashedTotal  int64   `json:"services_crashed_total"`
	ServicesDeferredTotal int64   `json:"services_deferred_total"`
	ReconcileErrorsTotal  int64   `json:"reconcile_errors_total"`
	ServicesRunning       int64   `json:"services_running"`
	ServicesInMaintenance int64   `json:"services_in_maintenance"`
}

// Snapshot returns a consistent point-in-time view of all metrics.
func (m *Metrics) Snapshot() Snapshot {
	return Snapshot{
		UptimeSeconds:         time.Since(m.startedAt).Seconds(),
		ReconcileCyclesTotal:  m.RecycleCyclesTotal.Load(),
		ServicesStartedTotal:  m.ServicesStartedTotal.Load(),
		ServicesStoppedTotal:  m.ServicesStoppedTotal.Load(),
		ServicesCrashedTotal:  m.ServicesCrashedTotal.Load(),
		ServicesDeferredTotal: m.ServicesDeferredTotal.Load(),
		ReconcileErrorsTotal:  m.ReconcileErrorsTotal.Load(),
		ServicesRunning:       m.ServicesRunning.Load(),
		ServicesInMaintenance: m.ServicesInMaintenance.Load(),
	}
}
