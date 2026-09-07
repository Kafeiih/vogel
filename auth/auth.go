// Package auth defines the contracts for authenticating an inbound HTTP request.
//
// The Authenticator interface abstracts token verification against an
// identity provider (e.g. Zitadel via OIDC). This package is dependency-free:
// it declares the port only. Concrete adapters (e.g. the Zitadel-backed
// implementation in auth/zitadel) live in subpackages so that a consumer
// importing only this port does not pull in an SDK it does not need.
//
// Deliberately excluded: org_id-based multi-tenancy. The systems this package
// was extracted from carried a Principal.OrgID mirrored from a Zitadel claim,
// intended for Cerbos derived-role checks — but the owner's Zitadel instance
// has exactly one organization, so no such comparison could ever discriminate
// between users, and org_id never scoped a single query or migration in
// either source system. The real separation between systems is OIDC audience
// validation, which an Authenticator implementation already performs.
package auth

import (
	"context"
	"errors"
	"net/http"
)

// Principal is the authenticated caller extracted from a validated request.
type Principal struct {
	UserID   string
	Username string
	Roles    []string
}

// HasRole reports whether the principal has been granted the given role.
// Safe to call on a nil Principal (returns false).
func (p *Principal) HasRole(role string) bool {
	if p == nil {
		return false
	}
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Authenticator verifies an inbound HTTP request and extracts its Principal.
//
// It takes the *http.Request, rather than a raw token string, so an
// implementation is free to read credentials from wherever the deployment
// puts them — the "Authorization" header in every adapter shipped today, but
// potentially a cookie or a different header for a future one — without
// widening this port's signature or leaking a transport detail into callers
// that only ever have a request in hand (namely httpx/middleware.Authenticate).
//
// Implementations MUST return one of the sentinel errors below (optionally
// wrapped) on failure, so httpx/middleware can map it to the correct HTTP
// status without importing the concrete provider SDK.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (*Principal, error)
}

var (
	// ErrUnauthenticated indicates the request carries no valid credentials:
	// missing, malformed, or expired token. Maps to HTTP 401.
	ErrUnauthenticated = errors.New("auth: unauthenticated")

	// ErrForbidden indicates the request carries valid credentials that still
	// fail a requirement the Authenticator itself enforces (e.g. a role
	// required at token-verification time, distinct from the resource-level
	// authorization performed by authz.Checker). Maps to HTTP 403.
	ErrForbidden = errors.New("auth: forbidden")

	// ErrServiceUnavailable indicates the identity provider could not be
	// reached or is failing (5xx), as opposed to the credentials being
	// invalid. Maps to HTTP 503, never 403 or 500: a provider outage reported
	// as 403 tells the client "you lack permission" and tells monitoring
	// "permissions bug" instead of "infrastructure incident", and unlike 403,
	// 503 is retryable. See httpx/middleware.Authenticate for the mapping.
	ErrServiceUnavailable = errors.New("auth: identity provider unavailable")
)
