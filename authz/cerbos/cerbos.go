// Package cerbos adapts github.com/cerbos/cerbos-sdk-go's gRPC client to the
// authz.Checker port.
package cerbos

import (
	"context"
	"fmt"
	"time"

	"github.com/cerbos/cerbos-sdk-go/cerbos"
	"google.golang.org/grpc/stats"

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
	// CallTimeout bounds every IsAllowed call, and a Ping whose context has
	// no deadline (a Ping with its own deadline uses that one; see Ping).
	// Zero uses DefaultCallTimeout; a negative value makes New fail. It is
	// applied on top of the caller's context, so a shorter caller deadline
	// still wins: the timeout only guarantees that a PDP which accepts the
	// connection and never answers cannot hang a request whose context has
	// no deadline. A timed-out call returns an error like any other PDP
	// failure, which the access layer and RequirePermission map to 503.
	CallTimeout time.Duration
	// StatsHandler, if set, is installed on the gRPC connection to the PDP
	// and sees every call (otelgrpc.NewClientHandler, for instance, traces
	// them). nil installs none.
	StatsHandler stats.Handler
}

// DefaultCallTimeout is the per-call deadline used when Config.CallTimeout
// is zero. A healthy PDP answers a check in single-digit milliseconds, so
// two seconds is generous for one decision while still failing a wedged PDP
// fast enough to answer 503 instead of holding the request open.
const DefaultCallTimeout = 2 * time.Second

// Checker implements authz.Checker using the Cerbos PDP via gRPC.
type Checker struct {
	client      *cerbos.GRPCClient
	host        string
	callTimeout time.Duration
}

// New creates a new Cerbos-backed authorization checker.
func New(cfg Config) (*Checker, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("authz/cerbos: host is required")
	}
	if cfg.CallTimeout < 0 {
		return nil, fmt.Errorf("authz/cerbos: CallTimeout must not be negative, got %v", cfg.CallTimeout)
	}
	callTimeout := cfg.CallTimeout
	if callTimeout == 0 {
		callTimeout = DefaultCallTimeout
	}

	client, err := cerbos.New(cfg.Host, clientOptions(cfg)...)
	if err != nil {
		return nil, fmt.Errorf("authz/cerbos: connecting to %s: %w", cfg.Host, err)
	}

	return &Checker{client: client, host: cfg.Host, callTimeout: callTimeout}, nil
}

// clientOptions builds the cerbos-sdk-go options for cfg. TLS is the SDK
// default, so the plaintext option is added only on explicit opt-out.
func clientOptions(cfg Config) []cerbos.Opt {
	var opts []cerbos.Opt
	if cfg.Plaintext {
		opts = append(opts, cerbos.WithPlaintext())
	}
	if cfg.StatsHandler != nil {
		opts = append(opts, cerbos.WithStatsHandler(cfg.StatsHandler))
	}
	return opts
}

// IsAllowed implements authz.Checker. A non-nil error means Cerbos could not
// be reached or failed to evaluate the request — the caller must map that to
// HTTP 503, not 403; see the doc comment on authz.Checker. The call is
// bounded by Config.CallTimeout (or a shorter deadline on ctx); a timeout is
// such an error.
func (c *Checker) IsAllowed(ctx context.Context, p authz.Principal, res authz.Resource, action string) (bool, error) {
	principal := cerbos.NewPrincipal(p.ID, p.Roles...)
	if len(p.Attr) > 0 {
		principal = principal.WithAttributes(p.Attr)
	}

	resource := cerbos.NewResource(res.Kind, res.ID)
	if len(res.Attr) > 0 {
		resource = resource.WithAttributes(res.Attr)
	}

	ctx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()

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
// 503 on every authorization.
//
// When ctx carries a deadline, that deadline alone bounds the call, even when
// it is longer than Config.CallTimeout: a startup probe that chooses to wait
// for the PDP to come up must not be cut short by a budget sized for the
// request path. When ctx has no deadline, Config.CallTimeout bounds the call,
// so a PDP that accepts the connection but never answers fails Ping instead
// of blocking startup. IsAllowed, by contrast, always applies CallTimeout.
func (c *Checker) Ping(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.callTimeout)
		defer cancel()
	}

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
