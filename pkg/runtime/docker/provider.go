// @nexus-project: nexus
// @nexus-path: pkg/runtime/docker/provider.go
// Package docker implements the runtime.Provider interface using the Docker Engine API.
//
// Design decisions:
//
//  1. CONTAINER NAMING: nexus.<project>.<service-id>
//     Dots as separators — Docker allows dots, no user container uses this pattern.
//     Example: nexus.ums.identity-api
//     Zero conflict with any docker-compose or manual container.
//
//  2. OWNERSHIP LABELS: every container Nexus creates carries two labels:
//     nexus.managed=true          → "Nexus owns this container"
//     nexus.service=<service-id>  → exact service identity
//     IsRunning filters by label only — survives renames, engine restarts,
//     docker system prune (which respects labels via --filter).
//
//  3. CONFIG SCHEMA (svc.Config is JSON):
//     {
//       "image":   "postgres:16-alpine",
//       "ports":   ["5432:5432"],
//       "env":     ["POSTGRES_PASSWORD=secret"],
//       "volumes": ["pgdata:/var/lib/postgresql/data"],
//       "network": "nexus-net"
//     }
//     All fields are optional except image.
//     Parsed by dockerConfig struct — unknown fields are silently ignored.
//
//  4. NETWORK: if config.Network is set, Nexus attaches the container to that
//     network. If the network does not exist, Nexus creates it first.
//     Default network is "nexus-net".
//
//  5. START IDEMPOTENCY: if a stopped container already exists (e.g. daemon
//     crashed mid-start), Start() removes it and creates a fresh one.
//     This avoids "container name already in use" errors.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"github.com/Harshmaury/Nexus/internal/state"
)

// ── LABELS ───────────────────────────────────────────────────────────────────

const (
	labelManaged   = "nexus.managed"   // value: "true"
	labelServiceID = "nexus.service"   // value: svc.ID
	labelProject   = "nexus.project"   // value: svc.Project
	defaultNetwork = "nexus-net"
)

// ── CONFIG ───────────────────────────────────────────────────────────────────

// dockerConfig is the schema for svc.Config JSON for docker-provider services.
// All fields except Image are optional.
//
// Example .nexus.yaml:
//
//	services:
//	  - id: identity-api
//	    provider: docker
//	    config:
//	      image: "harshmaury/ums-identity:latest"
//	      ports: ["5002:80"]
//	      env:   ["ASPNETCORE_ENVIRONMENT=Development"]
//	      volumes: ["identity-data:/app/data"]
//	      network: "ums-net"
type dockerConfig struct {
	Image   string   `json:"image"`
	Ports   []string `json:"ports"`   // host:container format, e.g. "5432:5432"
	Env     []string `json:"env"`     // KEY=VALUE format
	Volumes []string `json:"volumes"` // name:path or host:path format
	Network string   `json:"network"` // docker network name; defaults to nexus-net
}

// ── PROVIDER ─────────────────────────────────────────────────────────────────

// Provider implements runtime.Provider for Docker.
// Thread-safe — the Docker client is goroutine-safe.
type Provider struct {
	client *client.Client
}

// New creates a Docker Provider connecting to the local Docker engine.
// Fails fast if the engine is unreachable — caller should not register
// this provider if Docker is not available.
func New() (*Provider, error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker: connect to engine: %w", err)
	}
	return &Provider{client: cli}, nil
}

// Name returns the provider identifier used in logs and state.
func (p *Provider) Name() string {
	return "docker"
}

// ── START ────────────────────────────────────────────────────────────────────

// Start pulls the image if absent, ensures the network exists,
// removes any stale stopped container, then creates and starts a fresh one.
func (p *Provider) Start(ctx context.Context, svc *state.Service) error {
	cfg, err := parseConfig(svc.Config)
	if err != nil {
		return fmt.Errorf("docker: parse config for %s: %w", svc.ID, err)
	}

	if err := p.ensureImage(ctx, cfg.Image); err != nil {
		return fmt.Errorf("docker: ensure image %s: %w", cfg.Image, err)
	}

	networkName := cfg.Network
	if networkName == "" {
		networkName = defaultNetwork
	}
	if err := p.ensureNetwork(ctx, networkName); err != nil {
		return fmt.Errorf("docker: ensure network %s: %w", networkName, err)
	}

	containerName := containerName(svc)

	// Remove any stale stopped container so CreateContainer never hits
	// "name already in use". Running containers are left alone — IsRunning
	// will return true and Start will not be called again.
	if err := p.removeIfStopped(ctx, containerName); err != nil {
		return fmt.Errorf("docker: remove stale container %s: %w", containerName, err)
	}

	portBindings, exposedPorts, err := parsePorts(cfg.Ports)
	if err != nil {
		return fmt.Errorf("docker: parse ports for %s: %w", svc.ID, err)
	}

	binds, err := parseVolumes(cfg.Volumes)
	if err != nil {
		return fmt.Errorf("docker: parse volumes for %s: %w", svc.ID, err)
	}

	labels := map[string]string{
		labelManaged:   "true",
		labelServiceID: svc.ID,
		labelProject:   svc.Project,
	}

	containerCfg := &container.Config{
		Image:        cfg.Image,
		Env:          cfg.Env,
		ExposedPorts: exposedPorts,
		Labels:       labels,
	}

	hostCfg := &container.HostConfig{
		PortBindings:  portBindings,
		Binds:         binds,
		RestartPolicy: container.RestartPolicy{Name: "no"}, // Nexus manages restarts
	}

	networkCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: {},
		},
	}

	resp, err := p.client.ContainerCreate(ctx, containerCfg, hostCfg, networkCfg, nil, containerName)
	if err != nil {
		return fmt.Errorf("docker: create container %s: %w", containerName, err)
	}

	if err := p.client.ContainerStart(ctx, resp.ID, dockertypes.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("docker: start container %s: %w", containerName, err)
	}

	return nil
}

// ── STOP ─────────────────────────────────────────────────────────────────────

// Stop gracefully stops the container, then removes it.
// Removing is intentional — Nexus owns the lifecycle. Stopped containers
// are not kept around; they are recreated fresh on the next Start().
func (p *Provider) Stop(ctx context.Context, svc *state.Service) error {
	id, err := p.findContainerID(ctx, svc.ID)
	if err != nil {
		return fmt.Errorf("docker: find container for %s: %w", svc.ID, err)
	}
	if id == "" {
		return nil // already gone — idempotent
	}

	timeout := 10 // seconds
	if err := p.client.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout}); err != nil {
		// Log but continue to remove — container may already be stopped.
		_ = err
	}

	if err := p.client.ContainerRemove(ctx, id, dockertypes.ContainerRemoveOptions{
		RemoveVolumes: false, // preserve named volumes — data is not ours to delete
		Force:         true,
	}); err != nil {
		return fmt.Errorf("docker: remove container %s: %w", id, err)
	}

	return nil
}

// ── IS RUNNING ───────────────────────────────────────────────────────────────

// IsRunning returns true if a container with nexus.service=<svc.ID> label
// exists and is in "running" state.
// Label-based — survives docker rename, engine restarts, and manual stops.
func (p *Provider) IsRunning(ctx context.Context, svc *state.Service) (bool, error) {
	id, err := p.findContainerID(ctx, svc.ID)
	if err != nil {
		return false, fmt.Errorf("docker: IsRunning for %s: %w", svc.ID, err)
	}
	return id != "", nil
}

// ── INTERNAL HELPERS ─────────────────────────────────────────────────────────

// findContainerID returns the container ID of the running container owned by svc.
// Returns "" if no running container is found. Only "running" state counts.
func (p *Provider) findContainerID(ctx context.Context, serviceID string) (string, error) {
	f := filters.NewArgs(
		filters.Arg("label", fmt.Sprintf("%s=%s", labelServiceID, serviceID)),
		filters.Arg("label", fmt.Sprintf("%s=true", labelManaged)),
		filters.Arg("status", "running"),
	)

	containers, err := p.client.ContainerList(ctx, dockertypes.ContainerListOptions{
		Filters: f,
	})
	if err != nil {
		return "", fmt.Errorf("container list: %w", err)
	}

	if len(containers) == 0 {
		return "", nil
	}
	return containers[0].ID, nil
}

// removeIfStopped removes a container by name only if it is in a non-running state.
// Running containers are left completely untouched.
func (p *Provider) removeIfStopped(ctx context.Context, name string) error {
	f := filters.NewArgs(
		filters.Arg("name", "^/"+name+"$"),
	)
	containers, err := p.client.ContainerList(ctx, dockertypes.ContainerListOptions{
		All:     true, // include stopped
		Filters: f,
	})
	if err != nil {
		return fmt.Errorf("list by name: %w", err)
	}

	for _, c := range containers {
		if c.State == "running" {
			continue // never touch running containers
		}
		if err := p.client.ContainerRemove(ctx, c.ID, dockertypes.ContainerRemoveOptions{
			Force: true,
		}); err != nil {
			return fmt.Errorf("remove stale %s: %w", c.ID[:12], err)
		}
	}
	return nil
}

// ensureImage pulls the image if it is not already present locally.
// Pull output is discarded — this is a background operation.
func (p *Provider) ensureImage(ctx context.Context, image string) error {
	// Check local first — avoid a network call for images already present.
	_, _, err := p.client.ImageInspectWithRaw(ctx, image)
	if err == nil {
		return nil // already present
	}

	reader, err := p.client.ImagePull(ctx, image, dockertypes.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	defer reader.Close()
	_, _ = io.Copy(io.Discard, reader) // must drain to completion
	return nil
}

// ensureNetwork creates a bridge network if it does not already exist.
func (p *Provider) ensureNetwork(ctx context.Context, name string) error {
	f := filters.NewArgs(filters.Arg("name", name))
	networks, err := p.client.NetworkList(ctx, dockertypes.NetworkListOptions{Filters: f})
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	if len(networks) > 0 {
		return nil // already exists
	}

	_, err = p.client.NetworkCreate(ctx, name, dockertypes.NetworkCreate{
		Driver: "bridge",
		Labels: map[string]string{labelManaged: "true"},
	})
	return err
}

// ── CONFIG PARSING ────────────────────────────────────────────────────────────

func parseConfig(raw string) (dockerConfig, error) {
	if raw == "" || raw == "{}" {
		return dockerConfig{}, fmt.Errorf("config is empty — set at minimum: {\"image\": \"<image>:<tag>\"}")
	}
	var cfg dockerConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return dockerConfig{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if cfg.Image == "" {
		return dockerConfig{}, fmt.Errorf("config missing required field: image")
	}
	return cfg, nil
}

// parsePorts converts ["8080:80", "5432:5432"] to Docker port binding structures.
func parsePorts(ports []string) (nat.PortMap, nat.PortSet, error) {
	portMap := nat.PortMap{}
	portSet := nat.PortSet{}

	for _, p := range ports {
		parts := strings.SplitN(p, ":", 2)
		if len(parts) != 2 {
			return nil, nil, fmt.Errorf("invalid port format %q — expected host:container", p)
		}
		hostPort := parts[0]
		containerPort := nat.Port(parts[1] + "/tcp")

		portSet[containerPort] = struct{}{}
		portMap[containerPort] = []nat.PortBinding{
			{HostIP: "0.0.0.0", HostPort: hostPort},
		}
	}
	return portMap, portSet, nil
}

// parseVolumes converts ["pgdata:/var/lib/postgresql/data"] to Docker bind strings.
// Named volumes and host-path mounts are both passed through verbatim.
func parseVolumes(volumes []string) ([]string, error) {
	for _, v := range volumes {
		if !strings.Contains(v, ":") {
			return nil, fmt.Errorf("invalid volume format %q — expected name:path or /host:path", v)
		}
	}
	return volumes, nil
}

// containerName returns the stable Docker container name for a service.
// Format: nexus.<project>.<service-id>
// Dots are allowed by Docker and are never used in project/service IDs by Nexus.
func containerName(svc *state.Service) string {
	return fmt.Sprintf("nexus.%s.%s", svc.Project, svc.ID)
}
