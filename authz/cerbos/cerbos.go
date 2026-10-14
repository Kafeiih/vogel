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
//
// The connection uses TLS unless Plaintext is set: the zero value is the
// secure one, so a consumer cannot send authorization decisions in cleartext
// by forgetting a field.
type Config struct {
	// Host is the gRPC address of the Cerbos PDP, e.g. "localhost:3593".
	Host string
	// Plaintext disables TLS and connects over h2c. Only for a PDP reachable
	// on a trusted local network, such as a development container; never in
	// production, where principals, roles and decisions would travel in
	// cleartext.
	Plaintext bool
}

// Checker implements authz.Checker using the Cerbos PDP via gRPC.
type Checker struct {
	client *cerbos.GRPCClient
	host   string
}

// New creates a new Cerbos-backed authorization checker.
func New(cfg Config) (*Checker, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("authz/cerbos: host is required")
	}

	client, err := cerbos.New(cfg.Host, clientOptions(cfg)...)
	if err != nil {
		return nil, fmt.Errorf("authz/cerbos: connecting to %s: %w", cfg.Host, err)
	}

	return &Checker{client: client, host: cfg.Host}, nil
}

// clientOptions builds the cerbos-sdk-go options for cfg. TLS is the SDK
// default, so the plaintext option is added only on explicit opt-out.
func clientOptions(cfg Config) []cerbos.Opt {
	var opts []cerbos.Opt
	if cfg.Plaintext {
		opts = append(opts, cerbos.WithPlaintext())
	}
	return opts
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

// Ping asks the PDP for its server info, a cheap RPC that needs no policy,
// and returns an error naming the host when the call fails. New does not
// dial (the gRPC connection is lazy), so a wrong host or a transport mismatch
// (TLS against a plaintext PDP, or the reverse) only shows up on the first
// call; a consumer can call Ping at startup to fail fast instead of answering
// 503 on every authorization. Bound ctx: the call waits for it when the PDP
// accepts the connection but never answers.
func (c *Checker) Ping(ctx context.Context) error {
	if _, err := c.client.ServerInfo(ctx); err != nil {
		return fmt.Errorf("authz/cerbos: pinging %s: %w", c.host, err)
	}
	return nil
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
