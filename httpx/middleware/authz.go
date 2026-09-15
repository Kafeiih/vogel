package middleware

import (
	"log/slog"
	"net/http"

	"github.com/kafeiih/vogel/access"
	"github.com/kafeiih/vogel/authz"
)

// wildcardResourceID is the placeholder sent when the check does not target a
// concrete resource instance: collection routes (list) and creation routes,
// where the resource doesn't exist yet and therefore has no ID.
//
// This is NOT cosmetic. Cerbos rejects any resource with an empty ID
// ("id: value is required") before evaluating any policy, and that rejection
// surfaces as an HTTP 500 on every collection-level (list/create) route. The
// wildcard is a valid, non-empty ID that policies can match at the
// resource-kind level instead of a specific instance.
const wildcardResourceID = "*"

// RequirePermission returns a middleware that checks whether the Principal
// authenticated by an earlier Authenticate middleware is allowed to perform
// action on a resource of kind resourceKind.
//
// Use this when the authorization decision depends only on the principal and
// the resource kind — i.e. no resource-level attributes are needed. The
// resource ID is taken from the URL parameter "id" if present, and falls back
// to the wildcard otherwise.
//
// For attribute-based checks (e.g. status == "DRAFT"), or when the same
// Checker needs principal attributes resolved from another system (e.g. the
// user's current assignments), build an access.Guard and use RequireAccess
// here instead, then call access.Guard.Check + WriteAccessError in the
// handler after loading the entity.
//
// Fail-closed: on any error, this middleware writes an error response and
// returns without calling next.ServeHTTP.
//
// Status mapping — this deliberately supersedes go-licencias' DEC-08, which
// mapped a Cerbos check error to 403 "to avoid an oracle" (i.e. to keep a
// caller from telling a PDP outage apart from a real deny). That reasoning
// trades away more than it buys: a PDP outage reported as 403 tells the
// client "you lack permission" and tells monitoring "permissions bug"
// instead of "infrastructure incident", and unlike 403, 503 is retryable.
// go-crucible independently made the same mistake by returning 500 instead:
//   - checker.IsAllowed returns a non-nil error (PDP unreachable/failing) -> 503
//   - checker.IsAllowed returns (false, nil) (genuine denial)             -> 403
//   - no Principal in the request context (missing/invalid credentials)  -> 401
//
// The underlying error is always logged server-side at error level; its
// detail is never included in the response body.
//
// This is now a thin wrapper around RequireAccess, backed by an access.Guard
// with no PrincipalAttributes resolver configured — the status mapping and
// wildcard-ID logic live there once, instead of twice. One consequence of
// that: access.New panics on a nil checker, so a nil checker now surfaces
// immediately when RequirePermission is called (at router-construction time)
// rather than on the first request that hits the route it guards. That is a
// strictly earlier failure for what was already a wiring mistake, not a new
// way for this function to fail.
func RequirePermission(checker authz.Checker, resourceKind, action string, logger *slog.Logger, opts ...AuthOption) func(http.Handler) http.Handler {
	return RequireAccess(access.New(checker), resourceKind, action, logger, opts...)
}
