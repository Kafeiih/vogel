package auth

import "context"

// principalKey is the context key this package owns for storing the
// authenticated Principal. It is unexported so only WithPrincipal may set it,
// following the same pattern as reqctx.WithRequestID.
type principalKey struct{}

// WithPrincipal returns a copy of ctx carrying p as the authenticated principal.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the Principal stored in ctx by WithPrincipal, or nil if
// the request is unauthenticated (no Authenticate middleware ran, or it
// failed and returned before storing anything).
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}
