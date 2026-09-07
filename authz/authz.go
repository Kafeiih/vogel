// Package authz defines the contracts for authorization decisions.
//
// The Checker interface abstracts a policy decision point (PDP) such as
// Cerbos, OPA, or Casbin. This package is dependency-free: it declares the
// port only. Concrete adapters (e.g. the Cerbos-backed implementation in
// authz/cerbos) live in subpackages so that a consumer importing only this
// port does not pull in a PDP client it does not need.
//
// Deliberately excluded: org_id-based resource scoping. The source systems
// mirrored the principal's org_id onto every resource so Cerbos derived roles
// could compare request.principal.attr.org_id == request.resource.attr.org_id
// — but ResourceForUser copied that org_id straight off the same principal,
// so the comparison was always X == X, and the owner's single Zitadel
// organization meant there was never a second value to compare against
// anyway. See the vogel README for the full rationale.
package authz

import "context"

// Principal represents the authenticated user requesting access.
type Principal struct {
	ID    string
	Roles []string
	Attr  map[string]any
}

// Resource represents the object being accessed.
type Resource struct {
	Kind string // e.g. "invoices:invoice"
	ID   string
	Attr map[string]any
}

// Checker evaluates whether a principal is allowed to perform an action on a
// resource. Implementations may use Cerbos, OPA, Casbin, or any other PDP.
//
// A non-nil error means the decision could not be made at all — e.g. the PDP
// is unreachable or failing — and callers MUST treat that as distinct from a
// false allowed value: an error is an infrastructure problem (map it to HTTP
// 503), while allowed == false is a genuine authorization denial (map it to
// HTTP 403). Conflating the two — as all three source systems did — reports a
// PDP outage as a permissions bug instead of an infrastructure incident, and
// makes an outage look like a non-retryable client error. See
// httpx/middleware.RequirePermission for the mapping this port expects.
type Checker interface {
	IsAllowed(ctx context.Context, principal Principal, resource Resource, action string) (bool, error)
}
