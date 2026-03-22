// @nexus-project: nexus
// @nexus-path: internal/mode/mode.go
// ADR-044: runtime mode and capability visibility.
// Evaluator probes each capability at startup and on every reconcile
// cycle. Mode and capability state are exposed via GET /system/mode.
package mode

import (
	"net/http"
	"os"
	"sync"
	"time"
)

// RuntimeMode is the named operational state of the platform.
type RuntimeMode string

const (
	// ModeFull — identity enforced, all observers healthy.
	ModeFull RuntimeMode = "full"

	// ModeDegraded — core runtime operational, one or more optional
	// capabilities unavailable. Execution continues; findings may be stale.
	ModeDegraded RuntimeMode = "degraded"

	// ModeInsecure — identity capability absent. Gate is unreachable or
	// service-tokens file is missing. Execution continues but actor
	// attribution is disabled.
	ModeInsecure RuntimeMode = "insecure"
)

// CapabilityStatus is the availability state of one capability.
type CapabilityStatus string

const (
	CapabilityEnabled  CapabilityStatus = "enabled"
	CapabilityDisabled CapabilityStatus = "disabled"
	CapabilityDegraded CapabilityStatus = "degraded"
)

// CapabilityImpact describes how the absence of a capability affects mode.
type CapabilityImpact string

const (
	// ImpactRequired — absence sets mode to ModeInsecure.
	ImpactRequired CapabilityImpact = "required"
	// ImpactOptional — absence sets mode to ModeDegraded at worst.
	ImpactOptional CapabilityImpact = "optional"
)

// Capability is the status descriptor for one platform capability.
type Capability struct {
	Name   string           `json:"name"`
	Status CapabilityStatus `json:"status"`
	Source string           `json:"source"`
	Impact CapabilityImpact `json:"impact"`
	Reason string           `json:"reason,omitempty"`
}

// Snapshot is the result of one evaluation — mode + full capability list.
type Snapshot struct {
	Mode         RuntimeMode  `json:"mode"`
	Capabilities []Capability `json:"capabilities"`
	EvaluatedAt  time.Time    `json:"evaluated_at"`
}

// EvaluatorConfig holds addresses and flags needed to probe capabilities.
type EvaluatorConfig struct {
	GateAddr        string // e.g. "http://127.0.0.1:8088"
	GuardianAddr    string // e.g. "http://127.0.0.1:8085"
	SentinelAddr    string // e.g. "http://127.0.0.1:8087"
	HasServiceToken bool   // true if service-tokens file was loaded
	HasSSEBroker    bool   // true if SSE broker is attached
	HTTPTimeout     time.Duration
}

// Evaluator holds the latest capability snapshot and re-evaluates on demand.
type Evaluator struct {
	cfg      EvaluatorConfig
	mu       sync.RWMutex
	snapshot Snapshot
}

// NewEvaluator creates an Evaluator and runs the first evaluation immediately.
func NewEvaluator(cfg EvaluatorConfig) *Evaluator {
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 2 * time.Second
	}
	e := &Evaluator{cfg: cfg}
	e.Evaluate()
	return e
}

// Evaluate probes all capabilities and updates the internal snapshot.
// Safe to call from multiple goroutines — only one evaluation runs at a time.
func (e *Evaluator) Evaluate() Snapshot {
	caps := e.probe()
	mode := computeMode(caps)
	snap := Snapshot{
		Mode:         mode,
		Capabilities: caps,
		EvaluatedAt:  time.Now().UTC(),
	}
	e.mu.Lock()
	e.snapshot = snap
	e.mu.Unlock()
	return snap
}

// Snapshot returns the most recently computed snapshot without re-evaluating.
func (e *Evaluator) Snapshot() Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.snapshot
}

// probe checks each capability and returns the full list.
func (e *Evaluator) probe() []Capability {
	client := &http.Client{Timeout: e.cfg.HTTPTimeout}
	return []Capability{
		e.probeIdentity(client),
		e.probePolicy(client),
		e.probeInsights(client),
		e.probeAI(),
		e.probeSSE(),
	}
}

func (e *Evaluator) probeIdentity(client *http.Client) Capability {
	cap := Capability{
		Name:   "identity",
		Impact: ImpactRequired,
		Source: "-",
	}
	if !e.cfg.HasServiceToken {
		cap.Status = CapabilityDisabled
		cap.Reason = "service-tokens file not present"
		return cap
	}
	if e.cfg.GateAddr == "" {
		cap.Status = CapabilityDisabled
		cap.Reason = "GATE_HTTP_ADDR not configured"
		return cap
	}
	resp, err := client.Get(e.cfg.GateAddr + "/health")
	if err != nil {
		cap.Status = CapabilityDisabled
		cap.Reason = "Gate unreachable at " + e.cfg.GateAddr
		return cap
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cap.Status = CapabilityDegraded
		cap.Source = e.cfg.GateAddr
		cap.Reason = "Gate health returned non-200"
		return cap
	}
	cap.Status = CapabilityEnabled
	cap.Source = e.cfg.GateAddr
	return cap
}

func (e *Evaluator) probePolicy(client *http.Client) Capability {
	cap := Capability{
		Name:   "policy",
		Impact: ImpactOptional,
		Source: "-",
	}
	if e.cfg.GuardianAddr == "" {
		cap.Status = CapabilityDisabled
		cap.Reason = "GUARDIAN_HTTP_ADDR not configured"
		return cap
	}
	resp, err := client.Get(e.cfg.GuardianAddr + "/health")
	if err != nil {
		cap.Status = CapabilityDisabled
		cap.Reason = "Guardian unreachable"
		return cap
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cap.Status = CapabilityDegraded
		cap.Source = e.cfg.GuardianAddr
		cap.Reason = "Guardian health returned non-200"
		return cap
	}
	cap.Status = CapabilityEnabled
	cap.Source = e.cfg.GuardianAddr
	return cap
}

func (e *Evaluator) probeInsights(client *http.Client) Capability {
	cap := Capability{
		Name:   "insights",
		Impact: ImpactOptional,
		Source: "-",
	}
	if e.cfg.SentinelAddr == "" {
		cap.Status = CapabilityDisabled
		cap.Reason = "SENTINEL_HTTP_ADDR not configured"
		return cap
	}
	resp, err := client.Get(e.cfg.SentinelAddr + "/health")
	if err != nil {
		cap.Status = CapabilityDisabled
		cap.Reason = "Sentinel unreachable"
		return cap
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cap.Status = CapabilityDegraded
		cap.Source = e.cfg.SentinelAddr
		cap.Reason = "Sentinel health returned non-200"
		return cap
	}
	cap.Status = CapabilityEnabled
	cap.Source = e.cfg.SentinelAddr
	return cap
}

func (e *Evaluator) probeAI() Capability {
	cap := Capability{
		Name:   "ai",
		Impact: ImpactOptional,
		Source: "local",
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		cap.Status = CapabilityDisabled
		cap.Source = "-"
		cap.Reason = "ANTHROPIC_API_KEY not set"
		return cap
	}
	cap.Status = CapabilityEnabled
	return cap
}

func (e *Evaluator) probeSSE() Capability {
	cap := Capability{
		Name:   "sse",
		Impact: ImpactOptional,
		Source: "local",
	}
	if !e.cfg.HasSSEBroker {
		cap.Status = CapabilityDisabled
		cap.Source = "-"
		cap.Reason = "SSE broker not attached"
		return cap
	}
	cap.Status = CapabilityEnabled
	return cap
}

// computeMode derives RuntimeMode from a capability list.
func computeMode(caps []Capability) RuntimeMode {
	for _, c := range caps {
		if c.Impact == ImpactRequired &&
			(c.Status == CapabilityDisabled || c.Status == CapabilityDegraded) {
			return ModeInsecure
		}
	}
	for _, c := range caps {
		if c.Impact == ImpactOptional &&
			(c.Status == CapabilityDisabled || c.Status == CapabilityDegraded) {
			return ModeDegraded
		}
	}
	return ModeFull
}

// ModeLogLine returns the single-line startup summary string for engxd.
func (e *Evaluator) ModeLogLine() string {
	snap := e.Snapshot()
	if snap.Mode == ModeFull {
		return "runtime mode: full — all capabilities active"
	}
	line := "runtime mode: " + string(snap.Mode) + " — capabilities:"
	for _, c := range snap.Capabilities {
		line += " " + c.Name + "=" + string(c.Status)
	}
	return line
}
