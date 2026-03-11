// @nexus-project: nexus
// @nexus-path: pkg/runtime/provider.go
// Package runtime defines the Provider interface that all runtime backends implement.
// It lives in pkg/ so both internal/daemon and internal/controllers can import it
// without creating a cycle.
package runtime

import (
	"context"

	"github.com/Harshmaury/Nexus/internal/state"
)

// Provider is the runtime interface every backend must implement.
// Docker, K8s, and Process providers all satisfy this interface.
// The reconciler and health controller only ever call these methods.
type Provider interface {
	// Start launches the service and returns when it is running.
	Start(ctx context.Context, svc *state.Service) error

	// Stop gracefully shuts down the service.
	Stop(ctx context.Context, svc *state.Service) error

	// IsRunning checks whether the service is currently running.
	IsRunning(ctx context.Context, svc *state.Service) (bool, error)

	// Name returns the provider identifier for logging.
	Name() string
}

// Providers is a map of ProviderType to Provider implementation.
// Passed into Engine and HealthController at construction time.
type Providers map[state.ProviderType]Provider
