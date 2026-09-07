// Package auth defines the contracts for authenticating a caller from a
// bearer credential.
//
// The Authenticator interface abstracts token verification against an
// identity provider (e.g. Zitadel via OIDC). This package is dependency-free:
// it declares the port only, and imports nothing beyond the standard
// library's context and errors packages. Concrete adapters (e.g. the
// Zitadel-backed implementation in auth/zitadel) live in subpackages so that
// a consumer importing only this port does not pull in an SDK it does not
// need.
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

// Authenticator verifies a bearer token and extracts its Principal.
//
// It takes the raw token string, not an *http.Request. auth is the only port
// package in this module that would otherwise carry a dependency: authz
// imports only context, and storage and notification import nothing beyond
// the standard library either. A *http.Request parameter here existed solely
// so Authenticator could mention the type, but Go dependency graphs are
// per-package, not per-declaration — splitting Authenticator into its own
// file in this same package would not have helped. Every consumer that
// imports auth just to read a Principal, such as audit, inherited net/http
// transitively for no reason connected to what it actually does.
//
// A prior version of this doc defended *http.Request by arguing that a
// future adapter might need to read a cookie or a different header instead
// of "Authorization", and that a request parameter kept that choice open.
// That argument inverts once the port takes a token string: extracting the
// credential from wherever the deployment puts it is a transport concern,
// and it now lives entirely in httpx/middleware — the only place that has a
// live *http.Request in the first place. Moving to a cookie, or trying two
// header names, changes httpx/middleware only; this port's signature never
// moves for that reason. What a *http.Request parameter actually cost: every
// caller of Authenticate that is not inside an HTTP handler — a job running
// under a service identity, a CLI, a queue consumer in the project's worker
// binary — had to fabricate a fake *http.Request just to ask "who is this
// token for". A token string has no such caller.
//
// Implementations MUST return one of the sentinel errors below (optionally
// wrapped) on failure, so httpx/middleware can map it to the correct HTTP
// status without importing the concrete provider SDK.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*Principal, error)
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
