package middleware

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/httpx/response"
)

// Authenticate returns a middleware that authenticates the request via a and
// stores the resulting Principal in the request context (auth.WithPrincipal),
// so a later handler or middleware (e.g. RequirePermission) can read it via
// auth.FromContext.
//
// Fail-closed: on any error from a.Authenticate, this middleware writes an
// error response and returns without calling next.ServeHTTP.
//
// Status mapping:
//   - auth.ErrServiceUnavailable -> 503. The identity provider is unreachable
//     or failing; this must never collapse into 401 or 500. All three source
//     systems this package was ported from made that mistake — go-crucible
//     and go-licencias returned 403, and the base returned 403 too, for a
//     Zitadel outage. A 503 tells the client the failure is transient and
//     retryable and tells monitoring "infrastructure incident", where a 401
//     or 403 reads as "your credentials/permissions are the problem".
//   - auth.ErrForbidden -> 403
//   - anything else, including auth.ErrUnauthenticated -> 401
//
// The underlying error is always logged server-side at error level; its
// detail is never included in the response body.
func Authenticate(a auth.Authenticator, logger *slog.Logger, opts ...AuthOption) func(http.Handler) http.Handler {
	msgs := DefaultAuthMessages()
	for _, opt := range opts {
		opt(&msgs)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := a.Authenticate(r.Context(), r)
			if err != nil {
				logger.ErrorContext(r.Context(), "authentication failed", "error", err)

				switch {
				case errors.Is(err, auth.ErrServiceUnavailable):
					response.Error(w, r, http.StatusServiceUnavailable, response.CodeServiceUnavailable, msgs.ServiceUnavailable)
				case errors.Is(err, auth.ErrForbidden):
					response.Error(w, r, http.StatusForbidden, response.CodeForbidden, msgs.Forbidden)
				default:
					response.Error(w, r, http.StatusUnauthorized, response.CodeUnauthorized, msgs.Unauthorized)
				}
				return
			}

			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
		})
	}
}
