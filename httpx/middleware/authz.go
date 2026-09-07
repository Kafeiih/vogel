package middleware

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
	"github.com/kafeiih/vogel/httpx/response"
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
// For attribute-based checks (e.g. status == "DRAFT"), call
// authz.Checker.IsAllowed directly in the handler after loading the entity.
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
func RequirePermission(checker authz.Checker, resourceKind, action string, logger *slog.Logger, opts ...AuthOption) func(http.Handler) http.Handler {
	msgs := DefaultAuthMessages()
	for _, opt := range opts {
		opt(&msgs)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := auth.FromContext(r.Context())
			if principal == nil {
				response.Error(w, r, http.StatusUnauthorized, response.CodeUnauthorized, msgs.Unauthorized)
				return
			}

			p := authz.Principal{
				ID:    principal.UserID,
				Roles: principal.Roles,
			}

			resourceID := chi.URLParam(r, "id")
			if resourceID == "" {
				resourceID = wildcardResourceID
			}
			resource := authz.Resource{
				Kind: resourceKind,
				ID:   resourceID,
			}

			allowed, err := checker.IsAllowed(r.Context(), p, resource, action)
			if err != nil {
				logger.ErrorContext(r.Context(), "authorization check failed",
					"error", err,
					"user_id", principal.UserID,
					"resource_kind", resourceKind,
					"action", action,
				)
				response.Error(w, r, http.StatusServiceUnavailable, response.CodeServiceUnavailable, msgs.ServiceUnavailable)
				return
			}

			if !allowed {
				response.Error(w, r, http.StatusForbidden, response.CodeForbidden, msgs.Forbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
