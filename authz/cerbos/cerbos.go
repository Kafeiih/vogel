// Package cerbos adapts github.com/cerbos/cerbos-sdk-go's gRPC client to the
// authz.Checker port.
package cerbos

import (
	"context"
	"fmt"

	"github.com/cerbos/cerbos-sdk-go/cerbos"

	"github.com/kafeiih/vogel/authz"
)

// Config holds the settings needed to connect to a Cerbos PDP.
type Config struct {
	Host   string // gRPC address of the Cerbos PDP, e.g. "localhost:3593"
	UseTLS bool   // whether to use TLS for the gRPC connection
}

// Checker implements authz.Checker using the Cerbos PDP via gRPC.
type Checker struct {
	client *cerbos.GRPCClient
}

// New creates a new Cerbos-backed authorization checker.
func New(cfg Config) (*Checker, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("authz/cerbos: host is required")
	}

	var opts []cerbos.Opt
	if !cfg.UseTLS {
		opts = append(opts, cerbos.WithPlaintext())
	}

	client, err := cerbos.New(cfg.Host, opts...)
	if err != nil {
		return nil, fmt.Errorf("authz/cerbos: connecting to %s: %w", cfg.Host, err)
	}

	return &Checker{client: client}, nil
}

// IsAllowed implements authz.Checker. A non-nil error means Cerbos could not
// be reached or failed to evaluate the request — the caller must map that to
// HTTP 503, not 403; see the doc comment on authz.Checker.
func (c *Checker) IsAllowed(ctx context.Context, p authz.Principal, res authz.Resource, action string) (bool, error) {
	principal := cerbos.NewPrincipal(p.ID, p.Roles...)
	if len(p.Attr) > 0 {
		principal = principal.WithAttributes(p.Attr)
	}

	resource := cerbos.NewResource(res.Kind, res.ID)
	if len(res.Attr) > 0 {
		resource = resource.WithAttributes(res.Attr)
	}

	allowed, err := c.client.IsAllowed(ctx, principal, resource, action)
	if err != nil {
		return false, fmt.Errorf("authz/cerbos: check (resource=%s, action=%s): %w", res.Kind, action, err)
	}

	return allowed, nil
}

// Close is a documented no-op. cerbos.GRPCClient in cerbos-sdk-go v0.3.17
// exposes no Close method, so there is no underlying gRPC connection this
// method could release — it does NOT tear down anything. It exists only so
// a consumer that manages several adapters uniformly (e.g. alongside
// storage.Storage or notification.Notifier) can keep Checker in the same
// shutdown list without a type assertion; a consumer that does not need that
// uniformity can skip calling it entirely.
func (c *Checker) Close() error {
	return nil
}
