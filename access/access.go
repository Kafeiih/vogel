// Package access is the per-instance authorization check callable from the
// application layer (the hexagonal use cases, not the transport layer) once
// an entity has already been loaded.
//
// It sits between two packages that must each stay narrow for opposite
// reasons. authz stays dependency-free: it declares only the Checker port
// and the Principal/Resource shapes, so a consumer that needs nothing but
// the contract does not pull in this package, auth, or a PDP client.
// httpx/middleware, on the other hand, can only ever perform a COARSE check:
// RequirePermission (and RequireAccess, built on this package) run before
// the handler has read anything from storage, so all they have to check
// against is the resource kind and, at best, an ID taken straight off the
// URL. A decision that depends on the entity's own data -- its owner, its
// status, whether it has already been submitted -- is simply not knowable
// at that point. The two ways around that both cost more than this package
// does: teaching the middleware to load the entity itself means every
// protected route pays for a second read of the same row the handler is
// about to load anyway (once for the check, once for the actual work), and
// skipping the middleware check entirely just to defer to the handler loses
// the fail-closed 401/403/503 status mapping that RequirePermission already
// gives every route for free. access.Guard is what a handler calls AFTER
// its own load, with the loaded entity's data folded into authz.Resource.Attr,
// so the same authz.Checker port backs both the coarse, pre-load check in
// httpx/middleware and the fine-grained, post-load check here -- without
// httpx/middleware, this package, or authz importing one another beyond what
// is declared above. Like vogel/audit, this package must never import chi,
// httpx, or net/http: `go list -deps ./access` in access/deps_test.go
// enforces that a Guard is callable from a use case that has never heard of
// HTTP at all -- a worker, a CLI, a queue consumer.
package access

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
)

// PrincipalAttributes resolves consumer-owned attributes for an authenticated
// principal -- e.g. the set of resource IDs the user currently owns or is
// assigned to, looked up in whatever system tracks that assignment. The
// returned map becomes authz.Principal.Attr for every check made against
// that principal.
//
// This is deliberately a function type, not an interface: the lookup is
// almost always a single call into an already-wired repository or client
// (see examples/api for one built from a store query), and a one-method
// interface would only add a name to implement without buying anything a
// closure does not already give the caller.
type PrincipalAttributes func(ctx context.Context, p *auth.Principal) (map[string]any, error)

// Option configures a Guard built by New.
type Option func(*Guard)

// WithPrincipalAttributes configures the resolver Guard.Principal calls to
// populate authz.Principal.Attr. A nil fn is the same as not passing this
// option at all: the Guard behaves exactly as it did before this package
// supported attribute resolution, and Attr is always left nil.
func WithPrincipalAttributes(fn PrincipalAttributes) Option {
	return func(g *Guard) {
		g.attributes = fn
	}
}

// Guard performs per-instance authorization checks against a authz.Checker,
// optionally resolving principal attributes first. Build one with New.
type Guard struct {
	checker    authz.Checker
	attributes PrincipalAttributes
}

// New builds a Guard backed by checker, applying every opt in order.
//
// checker must not be nil: a Guard with no way to reach a PDP is a wiring
// mistake, not a runtime condition a caller could sensibly recover from, so
// New panics immediately instead of deferring the failure to the first
// Check call (where it would otherwise surface as a confusing nil pointer
// dereference deep inside an unrelated request).
func New(checker authz.Checker, opts ...Option) *Guard {
	if checker == nil {
		panic("access: nil authz.Checker")
	}
	g := &Guard{checker: checker}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// HasPrincipalAttributes reports whether a PrincipalAttributes resolver was
// configured via WithPrincipalAttributes.
//
// httpx/middleware.RequireAccess uses this to decide whether it needs to
// install a per-request memoization scope (WithRequestScope) before calling
// Check: when no resolver is configured, Principal never does any extra
// work, so installing a scope would add bookkeeping to a hot path with
// nothing for it to memoize. Exported (rather than kept package-private)
// because that decision has to be made from httpx/middleware, a different
// package, and Go has no way to expose a capability across a package
// boundary except through an exported name.
func (g *Guard) HasPrincipalAttributes() bool {
	return g.attributes != nil
}

// Principal resolves the authz.Principal for the caller stored in ctx by an
// earlier auth middleware: ID and Roles come straight from auth.Principal,
// and Attr is populated by the configured PrincipalAttributes resolver, if
// any.
//
// Two failure modes are distinguished, because they map to different HTTP
// statuses one layer up: no auth.Principal in ctx means the caller was never
// authenticated at all and wraps ErrUnauthenticated, while a resolver error
// means the caller IS authenticated but this Guard could not finish deciding
// and wraps ErrUnavailable together with the resolver's own error (via a
// second %w, so both errors.Is(err, ErrUnavailable) and errors.Is(err, cause)
// hold).
func (g *Guard) Principal(ctx context.Context) (authz.Principal, error) {
	ap := auth.FromContext(ctx)
	if ap == nil {
		return authz.Principal{}, ErrUnauthenticated
	}

	if g.attributes == nil {
		return authz.Principal{ID: ap.UserID, Roles: ap.Roles}, nil
	}

	attr, err := g.resolveAttributes(ctx, ap)
	if err != nil {
		return authz.Principal{}, err
	}
	return authz.Principal{ID: ap.UserID, Roles: ap.Roles, Attr: attr}, nil
}

// Check resolves the caller's authz.Principal (see Principal) and asks
// g.checker whether it may perform action on resource, returning:
//
//   - nil, when the checker allows the action;
//   - an error wrapping ErrUnauthenticated, when ctx carries no principal;
//   - ErrForbidden, when the checker returns (false, nil) -- a genuine
//     policy denial;
//   - an error wrapping ErrUnavailable (and the underlying cause), when
//     either the principal-attribute resolver or the checker itself fails --
//     an infrastructure problem, not a denial.
//
// resource, Attr included, is passed through to the checker exactly as
// given: Check does not substitute the collection wildcard for an empty
// resource ID the way httpx/middleware's coarse check does, because that
// substitution only makes sense before an entity exists to have an ID at
// all -- a route-shape concern the middleware owns, not this package.
func (g *Guard) Check(ctx context.Context, resource authz.Resource, action string) error {
	principal, err := g.Principal(ctx)
	if err != nil {
		return err
	}

	allowed, err := g.checker.IsAllowed(ctx, principal, resource, action)
	if err != nil {
		return fmt.Errorf("%w: check %q on %s/%s: %w", ErrUnavailable, action, resource.Kind, resource.ID, err)
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

// resolveAttributes calls g.attributes, memoizing the result (success or
// error) in the *requestScope carried by ctx, if any -- see WithRequestScope.
func (g *Guard) resolveAttributes(ctx context.Context, p *auth.Principal) (map[string]any, error) {
	scope, ok := ctx.Value(scopeKey{}).(*requestScope)
	if !ok {
		return g.callResolver(ctx, p)
	}

	key := scopeEntryKey{guard: g, principal: p}

	// The lock is held across the resolver call itself, not just around the
	// map access. That is intentional, not an oversight: it is what makes
	// two goroutines racing to resolve the SAME (guard, principal) pair --
	// e.g. RequireAccess's coarse check and the handler's later per-instance
	// Check, running concurrently rather than sequentially -- serialize into
	// one resolver call instead of two, with the second simply reading back
	// what the first just stored. The mutex is not reentrant: a resolver must
	// never call Principal or Check on a ctx carrying the same scope, or it
	// deadlocks.
	scope.mu.Lock()
	defer scope.mu.Unlock()

	if entry, found := scope.entries[key]; found {
		return cloneAttr(entry.attr), entry.err
	}

	attr, err := g.callResolver(ctx, p)
	scope.entries[key] = &scopeEntry{attr: attr, err: err}
	return cloneAttr(attr), err
}

// callResolver invokes g.attributes once and normalizes its result: a
// resolver error is wrapped in ErrUnavailable, and a nil or empty map is
// normalized to nil so a Principal with no configured attributes and one
// whose resolver legitimately found nothing look identical.
func (g *Guard) callResolver(ctx context.Context, p *auth.Principal) (map[string]any, error) {
	result, err := g.attributes(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve principal attributes: %w", ErrUnavailable, err)
	}
	if len(result) == 0 {
		return nil, nil
	}
	// Cloned here, once, so the map stored in the scope's cache (and handed
	// back to every caller sharing it) is never the same map instance the
	// resolver returned -- a resolver that reused a mutable map across calls
	// could otherwise corrupt an entry another goroutine is about to read.
	return maps.Clone(result), nil
}

// cloneAttr returns a fresh shallow copy of m, or nil if m is nil.
//
// Every caller of resolveAttributes gets its own copy: nothing in the
// authz.Checker contract forbids an implementation from mutating
// Principal.Attr, and without this, a checker mutating the map on one call
// path could bleed into the map already cached for a sibling call path
// sharing the same request scope. The copy is shallow: nested values (e.g. a
// []string of assignment IDs) are still shared and must be treated as
// read-only.
func cloneAttr(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return maps.Clone(m)
}

// scopeKey is the context key this package owns for storing a *requestScope,
// following the same unexported-key pattern as auth.WithPrincipal.
type scopeKey struct{}

// requestScope memoizes resolved principal attributes for the lifetime of
// one request (or any other ctx tree rooted at a WithRequestScope call).
type requestScope struct {
	mu      sync.Mutex
	entries map[scopeEntryKey]*scopeEntry
}

// scopeEntryKey identifies one (Guard, principal) pair within a scope.
//
// Keying by the *auth.Principal pointer -- not, say, its UserID string --
// guards against a principal being replaced in ctx mid-request (a re-auth,
// or a context derived for an impersonation subrequest): the old pointer's
// cached entry simply stops being looked up rather than being reused for a
// different principal that happens to share a scope. Keying by *Guard
// guards against two Guards with different resolvers (e.g. one per
// authz.Checker in a process that talks to more than one PDP) sharing a
// request and silently reusing each other's resolved attributes.
type scopeEntryKey struct {
	guard     *Guard
	principal *auth.Principal
}

// scopeEntry is the memoized outcome of one resolver call: both the
// attributes and the error are cached, so a resolver that legitimately
// fails is not retried on every subsequent Check within the same scope.
type scopeEntry struct {
	attr map[string]any
	err  error
}

// WithRequestScope returns a copy of ctx carrying a memoization scope for
// resolved principal attributes. Idempotent: if ctx already carries a scope,
// it is returned unchanged rather than nesting a second one, so calling this
// more than once on the same ctx tree (or its descendants) is always safe.
//
// httpx/middleware.RequireAccess installs this automatically, once, when its
// Guard has a PrincipalAttributes resolver configured -- before its own
// coarse check runs -- so that the handler's later per-instance Check reuses
// the same resolved attributes instead of resolving them again.
//
// Without a scope in ctx, Guard.Principal resolves attributes fresh on every
// call: that is the correct behavior for a call site that is not part of an
// HTTP request at all (a worker, a CLI, a scheduled job), which has no
// natural "request" over which to amortize the resolver.
func WithRequestScope(ctx context.Context) context.Context {
	if _, ok := ctx.Value(scopeKey{}).(*requestScope); ok {
		return ctx
	}
	return context.WithValue(ctx, scopeKey{}, &requestScope{entries: make(map[scopeEntryKey]*scopeEntry)})
}

// Sentinel errors returned (optionally wrapped) by Guard.Principal and
// Guard.Check.
//
// These are this package's own sentinels rather than a reuse of auth's
// ErrServiceUnavailable or authz's own errors: auth.ErrServiceUnavailable's
// name and doc comment both specifically name the identity provider -- it is
// the contract of auth.Authenticator, which this package does not implement.
// What can fail here is different: the PDP (authz.Checker) or a consumer's
// own attribute resolver (PrincipalAttributes), neither of which is an
// identity provider. authz itself declares no sentinels at all -- its
// Checker contract only distinguishes an error from (false, nil), leaving
// the mapping to status codes to whoever consumes it, which is exactly the
// job these three sentinels do for this package's callers.
var (
	// ErrUnauthenticated indicates ctx carries no authenticated principal.
	ErrUnauthenticated = errors.New("access: unauthenticated")

	// ErrForbidden indicates the checker evaluated the request and denied
	// it: a genuine policy decision, not an infrastructure failure.
	ErrForbidden = errors.New("access: forbidden")

	// ErrUnavailable indicates the authorization decision could not be made
	// at all -- the PDP is unreachable or failing, or a configured
	// PrincipalAttributes resolver returned an error -- as opposed to the
	// request being genuinely denied.
	ErrUnavailable = errors.New("access: authorization decision unavailable")
)
