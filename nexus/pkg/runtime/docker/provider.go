// @nexus-project: nexus
// @nexus-path: pkg/runtime/docker/provider.go
// Package docker implements the runtime.Provider interface for Docker containers.
// It uses the Docker SDK (github.com/docker/docker) to manage container lifecycle.
//
// Container naming:   nexus.<project>.<service-id>
// Ownership tracking: labels nexus.managed=true and nexus.service=<id>
// IsRunning:          label filter only — survives container renames
// Config keys (svc.Config JSON): image, ports, env, volumes, network
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"

	"github.com/Harshmaury/Nexus/internal/state"
)

// ── CONSTANTS ────────────────────────────────────────────────────────────────

const (
	providerName       = "docker"
	labelManaged       = "nexus.managed"
	labelService       = "nexus.service"
	nexusNetwork       = "nexus-net"
	stopGracePeriod    = 10 * time.Second
	containerNameFmt   = "nexus.%s.%s" // nexus.<project>.<service-id>
)

// ── CONFIG ────────────────────────────────────────────────────────────────────

// ServiceConfig is the schema for svc.Config JSON for docker-provider services.
type ServiceConfig struct {
	Image   string            `json:"image"`
	Ports   []string          `json:"ports"`   // ["8080:80", "443:443"]
	Env     []string          `json:"env"`     // ["KEY=value"]
	Volumes []string          `json:"volumes"` // ["/host:/container"]
	Network string            `json:"network"` // defaults to nexus-net
	Labels  map[string]string `json:"labels"`
}

// ── PROVIDER ─────────────────────────────────────────────────────────────────

// Provider implements runtime.Provider for Docker.
type Provider struct {
	client *dockerclient.Client
}

// New creates a new Docker provider.
// Returns an error if the Docker daemon is unreachable — caller should
// log a warning and continue without registering this provider.
func New() (*Provider, error) {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := cli.Ping(ctx); err != nil {
		return nil, fmt.Errorf("docker daemon unreachable: %w", err)
	}

	return &Provider{client: cli}, nil
}

// Name implements runtime.Provider.
func (p *Provider) Name() string { return providerName }

// ── START ────────────────────────────────────────────────────────────────────

// Start pulls the image, ensures the nexus network exists, removes any stale
// stopped container with the same name, then creates and starts a fresh one.
func (p *Provider) Start(ctx context.Context, svc *state.Service) error {
	cfg, err := parseConfig(svc.Config)
	if err != nil {
		return fmt.Errorf("parse config for service %q: %w", svc.ID, err)
	}

	if err := p.pullImage(ctx, cfg.Image); err != nil {
		return fmt.Errorf("pull image %q: %w", cfg.Image, err)
	}

	if err := p.ensureNetwork(ctx, nexusNetwork); err != nil {
		return fmt.Errorf("ensure network %q: %w", nexusNetwork, err)
	}

	containerName := fmt.Sprintf(containerNameFmt, svc.Project, svc.ID)

	// Remove any stale stopped container so we get a clean start.
	if err := p.removeStoppedContainer(ctx, containerName); err != nil {
		return fmt.Errorf("remove stale container %q: %w", containerName, err)
	}

	portBindings, exposedPorts, err := parsePorts(cfg.Ports)
	if err != nil {
		return fmt.Errorf("parse ports for service %q: %w", svc.ID, err)
	}

	netName := cfg.Network
	if netName == "" {
		netName = nexusNetwork
	}

	labels := map[string]string{
		labelManaged: "true",
		labelService: svc.ID,
	}
	for k, v := range cfg.Labels {
		labels[k] = v
	}

	resp, err := p.client.ContainerCreate(ctx,
		&container.Config{
			Image:        cfg.Image,
			Env:          cfg.Env,
			Labels:       labels,
			ExposedPorts: exposedPorts,
		},
		&container.HostConfig{
			Binds:        cfg.Volumes,
			PortBindings: portBindings,
			RestartPolicy: container.RestartPolicy{Name: "no"},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				netName: {},
			},
		},
		nil,
		containerName,
	)
	if err != nil {
		return fmt.Errorf("create container %q: %w", containerName, err)
	}

	if err := p.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start container %q: %w", containerName, err)
	}

	return nil
}

// ── STOP ─────────────────────────────────────────────────────────────────────

// Stop finds the container by label (not name), gracefully stops it,
// then removes it. Volumes are preserved.
func (p *Provider) Stop(ctx context.Context, svc *state.Service) error {
	containerID, err := p.findContainerIDByLabel(ctx, svc.ID)
	if err != nil {
		return fmt.Errorf("find container for service %q: %w", svc.ID, err)
	}
	if containerID == "" {
		return nil // already gone — not an error
	}

	stopTimeout := int(stopGracePeriod.Seconds())
	if err := p.client.ContainerStop(ctx, containerID, container.StopOptions{
		Timeout: &stopTimeout,
	}); err != nil {
		return fmt.Errorf("stop container for service %q: %w", svc.ID, err)
	}

	if err := p.client.ContainerRemove(ctx, containerID, container.RemoveOptions{
		RemoveVolumes: false,
	}); err != nil {
		return fmt.Errorf("remove container for service %q: %w", svc.ID, err)
	}

	return nil
}

// ── IS RUNNING ───────────────────────────────────────────────────────────────

// IsRunning uses label filtering to check whether the container is running.
// Label-based — survives container renames and restarts.
func (p *Provider) IsRunning(ctx context.Context, svc *state.Service) (bool, error) {
	f := filters.NewArgs(
		filters.Arg("label", fmt.Sprintf("%s=%s", labelService, svc.ID)),
		filters.Arg("status", "running"),
	)

	containers, err := p.client.ContainerList(ctx, container.ListOptions{Filters: f})
	if err != nil {
		return false, fmt.Errorf("list containers for service %q: %w", svc.ID, err)
	}

	return len(containers) > 0, nil
}

// ── PRIVATE HELPERS ──────────────────────────────────────────────────────────

func (p *Provider) pullImage(ctx context.Context, imageName string) error {
	reader, err := p.client.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()
	_, err = io.Copy(io.Discard, reader) // drain — required for pull to complete
	return err
}

func (p *Provider) ensureNetwork(ctx context.Context, name string) error {
	f := filters.NewArgs(filters.Arg("name", name))
	networks, err := p.client.NetworkList(ctx, network.ListOptions{Filters: f})
	if err != nil {
		return err
	}
	for _, n := range networks {
		if n.Name == name {
			return nil // already exists
		}
	}

	_, err = p.client.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{labelManaged: "true"},
	})
	return err
}

func (p *Provider) removeStoppedContainer(ctx context.Context, name string) error {
	f := filters.NewArgs(
		filters.Arg("name", name),
		filters.Arg("status", "exited"),
		filters.Arg("status", "created"),
		filters.Arg("status", "dead"),
	)
	containers, err := p.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		return err
	}

	for _, c := range containers {
		if err := p.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) findContainerIDByLabel(ctx context.Context, serviceID string) (string, error) {
	f := filters.NewArgs(
		filters.Arg("label", fmt.Sprintf("%s=%s", labelService, serviceID)),
	)
	containers, err := p.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		return "", err
	}
	if len(containers) == 0 {
		return "", nil
	}
	return containers[0].ID, nil
}

func parsePorts(ports []string) (nat.PortMap, nat.PortSet, error) {
	portMap := nat.PortMap{}
	portSet := nat.PortSet{}

	for _, p := range ports {
		host, container_, err := nat.ParsePortSpec(p)
		if err != nil {
			return nil, nil, fmt.Errorf("parse port %q: %w", p, err)
		}
		portSet[container_.Port()] = struct{}{}
		portMap[container_.Port()] = append(portMap[container_.Port()], nat.PortBinding{
			HostIP:   host.HostIP,
			HostPort: host.HostPort,
		})
	}

	return portMap, portSet, nil
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
