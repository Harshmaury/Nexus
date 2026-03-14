// @nexus-project: nexus
// @nexus-path: pkg/runtime/k8s/provider.go
// Package k8s implements runtime.Provider using the kubectl binary.
// Uses scale-to-0 as the stop strategy — deployments are never deleted,
// only scaled down. This preserves ConfigMaps, Services, and PVCs.
//
// WHY KUBECTL BINARY, NOT client-go:
//   client-go adds ~30 transitive dependencies and requires managing kubeconfig
//   parsing, REST client setup, and informer caches. For a local Minikube setup
//   this is unnecessary overhead. kubectl is already in ~/bin and uses the same
//   kubeconfig as the developer — zero extra setup.
//   Upgrade path: replace exec calls with client-go when multi-cluster support
//   is needed (Phase 14: multi-machine agent mode).
//
// CONFIG SCHEMA (svc.Config is JSON):
//
//	{
//	  "namespace":  "default",
//	  "deployment": "identity-api",
//	  "replicas":   1,
//	  "kubeconfig": "~/.kube/config"
//	}
//
//	namespace   optional — defaults to "default"
//	deployment  required — name of the Kubernetes Deployment
//	replicas    optional — target replica count on Start; defaults to 1
//	kubeconfig  optional — path to kubeconfig; defaults to KUBECONFIG env or ~/.kube/config
package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	defaultNamespace = "default"
	defaultReplicas  = 1
	kubectlBinary    = "kubectl"
)

// ── CONFIG ───────────────────────────────────────────────────────────────────

// k8sConfig is the schema for svc.Config JSON for k8s-provider services.
type k8sConfig struct {
	Namespace  string `json:"namespace"`
	Deployment string `json:"deployment"` // required
	Replicas   int    `json:"replicas"`
	Kubeconfig string `json:"kubeconfig"`
}

func parseConfig(raw string) (k8sConfig, error) {
	if raw == "" || raw == "{}" {
		return k8sConfig{}, fmt.Errorf("k8s: config is empty — set at minimum: {\"deployment\":\"<name>\"}")
	}
	var cfg k8sConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return k8sConfig{}, fmt.Errorf("k8s: invalid config JSON: %w", err)
	}
	if cfg.Deployment == "" {
		return k8sConfig{}, fmt.Errorf("k8s: config missing required field: deployment")
	}
	if cfg.Namespace == "" {
		cfg.Namespace = defaultNamespace
	}
	if cfg.Replicas <= 0 {
		cfg.Replicas = defaultReplicas
	}
	return cfg, nil
}

// ── PROVIDER ─────────────────────────────────────────────────────────────────

// Provider implements runtime.Provider for Kubernetes Deployments.
// Thread-safe — all state is in the cluster.
type Provider struct {
	kubectlPath string
}

// New creates a K8s Provider, resolving the kubectl binary path.
// Checks ~/bin/kubectl first (Nexus convention), then $PATH.
func New() (*Provider, error) {
	kubectlPath, err := resolveKubectl()
	if err != nil {
		return nil, fmt.Errorf("k8s: kubectl not found — install kubectl and ensure it is in ~/bin or $PATH: %w", err)
	}
	return &Provider{kubectlPath: kubectlPath}, nil
}

// Name returns the provider identifier used in logs and state.
func (p *Provider) Name() string { return "k8s" }

// ── START ─────────────────────────────────────────────────────────────────────

// Start scales the Deployment up to the configured replica count.
// Idempotent: if already at target replicas, kubectl is a no-op.
func (p *Provider) Start(ctx context.Context, svc *state.Service) error {
	cfg, err := parseConfig(svc.Config)
	if err != nil {
		return err
	}

	_, err = p.kubectl(ctx, cfg,
		"scale", "deployment/"+cfg.Deployment,
		"--replicas="+strconv.Itoa(cfg.Replicas),
	)
	if err != nil {
		return fmt.Errorf("k8s: scale up %s/%s: %w", cfg.Namespace, cfg.Deployment, err)
	}
	return nil
}

// ── STOP ──────────────────────────────────────────────────────────────────────

// Stop scales the Deployment to 0 replicas.
// The Deployment object is preserved — only pods are terminated.
// Idempotent: scaling to 0 when already at 0 is a no-op.
func (p *Provider) Stop(ctx context.Context, svc *state.Service) error {
	cfg, err := parseConfig(svc.Config)
	if err != nil {
		return err
	}

	_, err = p.kubectl(ctx, cfg,
		"scale", "deployment/"+cfg.Deployment,
		"--replicas=0",
	)
	if err != nil {
		return fmt.Errorf("k8s: scale down %s/%s: %w", cfg.Namespace, cfg.Deployment, err)
	}
	return nil
}

// ── IS RUNNING ────────────────────────────────────────────────────────────────

// IsRunning returns true if the Deployment has at least one ready replica.
// Uses jsonpath to extract readyReplicas — single field, minimal output.
func (p *Provider) IsRunning(ctx context.Context, svc *state.Service) (bool, error) {
	cfg, err := parseConfig(svc.Config)
	if err != nil {
		return false, err
	}

	out, err := p.kubectl(ctx, cfg,
		"get", "deployment/"+cfg.Deployment,
		"-o", "jsonpath={.status.readyReplicas}",
	)
	if err != nil {
		// Deployment may not exist yet — not an error, just not running.
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, fmt.Errorf("k8s: check deployment %s/%s: %w", cfg.Namespace, cfg.Deployment, err)
	}

	trimmed := strings.TrimSpace(out)
	if trimmed == "" || trimmed == "0" {
		return false, nil
	}

	ready, err := strconv.Atoi(trimmed)
	if err != nil {
		return false, fmt.Errorf("k8s: parse readyReplicas %q for %s: %w", trimmed, cfg.Deployment, err)
	}
	return ready > 0, nil
}

// ── KUBECTL RUNNER ────────────────────────────────────────────────────────────

// kubectl runs a kubectl command with namespace and optional kubeconfig flags
// automatically prepended. Returns combined stdout output.
func (p *Provider) kubectl(ctx context.Context, cfg k8sConfig, args ...string) (string, error) {
	fullArgs := []string{"-n", cfg.Namespace}

	if cfg.Kubeconfig != "" {
		fullArgs = append(fullArgs, "--kubeconfig", expandHome(cfg.Kubeconfig))
	}

	fullArgs = append(fullArgs, args...)

	cmd := exec.CommandContext(ctx, p.kubectlPath, fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ── UTILITIES ────────────────────────────────────────────────────────────────

// resolveKubectl finds the kubectl binary.
// Checks ~/bin/kubectl first (Nexus workspace convention), then $PATH.
func resolveKubectl() (string, error) {
	home, err := os.UserHomeDir()
	if err == nil {
		local := filepath.Join(home, "bin", kubectlBinary)
		if _, statErr := os.Stat(local); statErr == nil {
			return local, nil
		}
	}
	return exec.LookPath(kubectlBinary)
}

// expandHome replaces a leading ~/ with the user home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}
