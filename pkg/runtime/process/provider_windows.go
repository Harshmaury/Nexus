// @nexus-project: nexus
// @nexus-path: pkg/runtime/process/provider_windows.go

//go:build windows

// Package process provides a no-op Provider stub for Windows.
// engxd does not run on Windows (ADR-028 Rule 8 by extension), but engx and
// engxa are cross-compiled for windows/amd64. The compiler requires all
// packages in the module to build on every target, so this stub satisfies
// the runtime.Provider interface without importing Unix-only syscalls.
package process

import (
	"context"
	"errors"

	"github.com/Harshmaury/Nexus/internal/state"
)

var errNotSupported = errors.New("process provider: not supported on Windows")

// Provider is a no-op stub. engxd does not run on Windows.
type Provider struct{}

// New returns a stub Provider. Always succeeds so the module compiles.
func New() (*Provider, error) {
	return &Provider{}, nil
}

// Name returns the provider identifier.
func (p *Provider) Name() string { return "process" }

// Start is not supported on Windows.
func (p *Provider) Start(_ context.Context, _ *state.Service) error {
	return errNotSupported
}

// Stop is not supported on Windows.
func (p *Provider) Stop(_ context.Context, _ *state.Service) error {
	return errNotSupported
}

// IsRunning is not supported on Windows.
func (p *Provider) IsRunning(_ context.Context, _ *state.Service) (bool, error) {
	return false, errNotSupported
}

