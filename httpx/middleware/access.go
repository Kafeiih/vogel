package middleware

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/kafeiih/vogel/access"
	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
	"github.com/kafeiih/vogel/httpx/response"
)

// RequireAccess returns a middleware that performs the same coarse
// authorization check as RequirePermission (principal + resource kind + an
// ID taken from the URL, with no entity-specific attributes), but through an
// access.Guard instead of a bare authz.Checker.
//
// Use this instead of RequirePermission when the same Checker also backs a
// per-instance check made later in the handler (via guard.Check, after the
// entity is loaded, with its data folded into authz.Resource.Attr) -- see
// the access package doc for why that second check cannot happen here, in
// the middleware, instead. When guard was built with
// access.WithPrincipalAttributes, this middleware installs
// access.WithRequestScope on the request context BEFORE running its own
// check, so the principal-attribute resolver runs at most once for the
// request no matter how many of the middleware's own check and the
// handler's later guard.Check end up needing it. When guard has no resolver
// configured, the request is passed to next completely unchanged -- byte
// for byte the same behavior as before this middleware existed.
//
// Fail-closed: on any error, this middleware writes a response (via
// WriteAccessError) and returns without calling next.ServeHTTP.
func RequireAccess(guard *access.Guard, resourceKind, action string, logger *slog.Logger, opts ...AuthOption) func(http.Handler) http.Handler {
	msgs := DefaultAuthMessages()
	for _, opt := range opts {
		opt(&msgs)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if guard.HasPrincipalAttributes() {
				r = r.WithContext(access.WithRequestScope(r.Context()))
			}

			resourceID := chi.URLParam(r, "id")
			if resourceID == "" {
				resourceID = wildcardResourceID
			}
			resource := authz.Resource{Kind: resourceKind, ID: resourceID}

			if err := guard.Check(r.Context(), resource, action); err != nil {
				writeAccessStatus(w, r, err, logger, msgs, resourceKind, action)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// WriteAccessError maps an error returned by access.Guard.Check (or
// access.Guard.Principal) to the appropriate HTTP status and writes it,
// returning true. It writes nothing and returns false for nil or for any
// error that is not one of access.ErrUnauthenticated, access.ErrForbidden,
// or access.ErrUnavailable, so a handler can fall through to its own
// domain-specific error mapping (e.g. errors.Is against a not-found or
// conflict sentinel) instead of this helper claiming an error it does not
// recognize.
//
// This is the counterpart, inside a handler, of what RequireAccess does in
// the router: a handler that calls guard.Check after loading an entity uses
// WriteAccessError to get the exact same 401/403/503 mapping and logging the
// middleware gives every route, without duplicating the switch itself.
func WriteAccessError(w http.ResponseWriter, r *http.Request, err error, logger *slog.Logger, opts ...AuthOption) bool {
	if err == nil {
		return false
	}

	msgs := DefaultAuthMessages()
	for _, opt := range opts {
		opt(&msgs)
	}

	return writeAccessStatus(w, r, err, logger, msgs, "", "")
}

// writeAccessStatus is the single status-mapping function shared by
// RequireAccess and WriteAccessError, so the two call paths cannot drift
// apart on which error maps to which status. resourceKind and action are
// used only for the error-level log line on ErrUnavailable; either or both
// may be empty (WriteAccessError, called from inside a handler that already
// knows less about the route than the middleware did, does not always have
// them).
//
// It returns false -- writing nothing -- for any error that is not one of
// the three access sentinels, so WriteAccessError can report "I did not
// handle this" to its caller.
func writeAccessStatus(w http.ResponseWriter, r *http.Request, err error, logger *slog.Logger, msgs AuthMessages, resourceKind, action string) bool {
	switch {
	case errors.Is(err, access.ErrUnauthenticated):
		response.Error(w, r, http.StatusUnauthorized, response.CodeUnauthorized, msgs.Unauthorized)
		return true

	case errors.Is(err, access.ErrForbidden):
		response.Error(w, r, http.StatusForbidden, response.CodeForbidden, msgs.Forbidden)
		return true

	case errors.Is(err, access.ErrUnavailable):
		userID := ""
		if p := auth.FromContext(r.Context()); p != nil {
			userID = p.UserID
		}
		logger.ErrorContext(r.Context(), "authorization check failed",
			"error", err,
			"user_id", userID,
			"resource_kind", resourceKind,
			"action", action,
		)
		response.Error(w, r, http.StatusServiceUnavailable, response.CodeServiceUnavailable, msgs.ServiceUnavailable)
		return true

	default:
		return false
	}
}
