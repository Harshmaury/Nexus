// @nexus-project: nexus
// @nexus-path: pkg/runtime/k8s/provider.go
// Package k8s implements runtime.Provider for Kubernetes workloads.
//
// Strategy: control services by scaling replicas, not by creating/deleting pods.
//   Start  → scale Deployment or StatefulSet to cfg.Replicas (default 1)
//   Stop   → scale to 0 — pods terminate, PVCs and ConfigMaps are preserved
//   IsRunning → readyReplicas >= 1
//
// Config loaded from kubeconfig at ~/.kube/config (standard kubectl location).
// Config keys (svc.Config JSON): namespace, deployment, statefulset,
//                                 replicas, healthPath.
//
// Works with Minikube out of the box — no special setup required.
package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	providerName        = "k8s"
	defaultNamespace    = "default"
	defaultReplicas     = int32(1)
	readyPollInterval   = 2 * time.Second
	readyPollTimeout    = 120 * time.Second
)

// ── CONFIG ────────────────────────────────────────────────────────────────────

// ServiceConfig is the schema for svc.Config JSON for k8s-provider services.
// Exactly one of Deployment or StatefulSet must be set.
type ServiceConfig struct {
	Namespace   string `json:"namespace"`    // k8s namespace, defaults to "default"
	Deployment  string `json:"deployment"`   // Deployment name (mutually exclusive with StatefulSet)
	StatefulSet string `json:"statefulset"`  // StatefulSet name (mutually exclusive with Deployment)
	Replicas    int32  `json:"replicas"`     // desired replica count when started, defaults to 1
	HealthPath  string `json:"healthPath"`   // informational only — used by health controller
}

// workloadType distinguishes Deployment from StatefulSet.
type workloadType string

const (
	workloadDeployment  workloadType = "deployment"
	workloadStatefulSet workloadType = "statefulset"
)

// ── PROVIDER ─────────────────────────────────────────────────────────────────

// Provider implements runtime.Provider for Kubernetes.
type Provider struct {
	client kubernetes.Interface
}

// New creates a K8s provider using ~/.kube/config.
// Returns an error if the kubeconfig cannot be loaded or the API server
// is unreachable — caller should log warning and continue.
func New() (*Provider, error) {
	kubeconfig := kubeconfigPath()

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig from %s: %w", kubeconfig, err)
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create k8s client: %w", err)
	}

	// Connectivity check — list namespaces requires minimal RBAC.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		return nil, fmt.Errorf("k8s API server unreachable: %w", err)
	}

	return &Provider{client: client}, nil
}

// NewWithClient creates a K8s provider with a pre-built client.
// Used in tests to inject a fake client.
func NewWithClient(client kubernetes.Interface) *Provider {
	return &Provider{client: client}
}

// Name implements runtime.Provider.
func (p *Provider) Name() string { return providerName }

// ── START ────────────────────────────────────────────────────────────────────

// Start scales the workload to cfg.Replicas (default 1) and waits for
// at least one pod to become ready.
func (p *Provider) Start(ctx context.Context, svc *state.Service) error {
	cfg, err := parseConfig(svc.Config)
	if err != nil {
		return fmt.Errorf("parse config for service %q: %w", svc.ID, err)
	}

	wt, name, ns := resolveWorkload(cfg)

	replicas := cfg.Replicas
	if replicas <= 0 {
		replicas = defaultReplicas
	}

	if err := p.scale(ctx, wt, ns, name, replicas); err != nil {
		return fmt.Errorf("scale %s/%s to %d: %w", ns, name, replicas, err)
	}

	// Wait for ready — respect ctx deadline.
	if err := p.waitReady(ctx, wt, ns, name); err != nil {
		return fmt.Errorf("wait for %s/%s to be ready: %w", ns, name, err)
	}

	return nil
}

// ── STOP ─────────────────────────────────────────────────────────────────────

// Stop scales the workload to 0 — pods terminate, PVCs and ConfigMaps are
// preserved. This is a fast, reversible operation.
func (p *Provider) Stop(ctx context.Context, svc *state.Service) error {
	cfg, err := parseConfig(svc.Config)
	if err != nil {
		return fmt.Errorf("parse config for service %q: %w", svc.ID, err)
	}

	wt, name, ns := resolveWorkload(cfg)

	if err := p.scale(ctx, wt, ns, name, 0); err != nil {
		return fmt.Errorf("scale %s/%s to 0: %w", ns, name, err)
	}

	return nil
}

// ── IS RUNNING ───────────────────────────────────────────────────────────────

// IsRunning returns true when readyReplicas >= 1.
func (p *Provider) IsRunning(ctx context.Context, svc *state.Service) (bool, error) {
	cfg, err := parseConfig(svc.Config)
	if err != nil {
		return false, fmt.Errorf("parse config for service %q: %w", svc.ID, err)
	}

	wt, name, ns := resolveWorkload(cfg)

	ready, err := p.readyReplicas(ctx, wt, ns, name)
	if err != nil {
		return false, err
	}

	return ready >= 1, nil
}

// ── PRIVATE: SCALE ───────────────────────────────────────────────────────────

// scale uses a JSON merge-patch so we only touch the replicas field.
func (p *Provider) scale(ctx context.Context, wt workloadType, ns, name string, replicas int32) error {
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))

	switch wt {
	case workloadDeployment:
		_, err := p.client.AppsV1().Deployments(ns).Patch(
			ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
		)
		return err

	case workloadStatefulSet:
		_, err := p.client.AppsV1().StatefulSets(ns).Patch(
			ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
		)
		return err

	default:
		return fmt.Errorf("unknown workload type %q", wt)
	}
}

// ── PRIVATE: READY CHECK ─────────────────────────────────────────────────────

func (p *Provider) readyReplicas(ctx context.Context, wt workloadType, ns, name string) (int32, error) {
	switch wt {
	case workloadDeployment:
		d, err := p.client.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return 0, fmt.Errorf("get deployment %s/%s: %w", ns, name, err)
		}
		return d.Status.ReadyReplicas, nil

	case workloadStatefulSet:
		s, err := p.client.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return 0, fmt.Errorf("get statefulset %s/%s: %w", ns, name, err)
		}
		return s.Status.ReadyReplicas, nil

	default:
		return 0, fmt.Errorf("unknown workload type %q", wt)
	}
}

// waitReady polls readyReplicas until >= 1 or context/timeout expires.
func (p *Provider) waitReady(ctx context.Context, wt workloadType, ns, name string) error {
	deadline := time.Now().Add(readyPollTimeout)
	pollCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	ticker := time.NewTicker(readyPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pollCtx.Done():
			return fmt.Errorf("timed out waiting for %s/%s to be ready", ns, name)
		case <-ticker.C:
			ready, err := p.readyReplicas(pollCtx, wt, ns, name)
			if err != nil {
				return err
			}
			if ready >= 1 {
				return nil
			}
		}
	}
}

// ── PRIVATE: HELPERS ─────────────────────────────────────────────────────────

// resolveWorkload returns (type, name, namespace) from config.
// Deployment takes precedence if both are set.
func resolveWorkload(cfg *ServiceConfig) (workloadType, string, string) {
	ns := cfg.Namespace
	if ns == "" {
		ns = defaultNamespace
	}

	if cfg.Deployment != "" {
		return workloadDeployment, cfg.Deployment, ns
	}
	return workloadStatefulSet, cfg.StatefulSet, ns
}

func parseConfig(raw string) (*ServiceConfig, error) {
	if raw == "" {
		return &ServiceConfig{}, nil
	}
	var cfg ServiceConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal service config: %w", err)
	}
	return &cfg, nil
}

func kubeconfigPath() string {
	if env := os.Getenv("KUBECONFIG"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kube", "config")
}

// ── UNUSED IMPORT ANCHOR ─────────────────────────────────────────────────────
// Keep appsv1 import alive — used indirectly through client methods.
var _ *appsv1.Deployment
