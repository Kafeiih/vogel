package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/httpx/response"
)

// bearerScheme is the RFC 6750 §2.1 "Authorization Request Header Field"
// scheme name. The "Bearer" auth-scheme token is compared case-insensitively,
// per RFC 7235 §2.1's case-insensitive auth-scheme rule.
const bearerScheme = "Bearer"

// extractBearerToken pulls the bearer token out of an incoming request's
// "Authorization" header, per RFC 6750 §2.1.
//
// This is the one place in the module that knows where a credential lives on
// the wire. auth.Authenticator takes a bare token string precisely so that
// this decision — header vs. cookie, "Bearer" vs. something else — stays a
// transport concern confined to this middleware: switching to a different
// header or a cookie would change only this function, never the port or any
// adapter.
//
// It returns ok == false for a missing header, an empty header, a scheme
// other than "Bearer" (case-insensitive), or a "Bearer" scheme with no token
// (or only whitespace) after it. strings.Fields splits on any run of
// whitespace and discards leading/trailing runs, so irregular spacing around
// the scheme or the token is tolerated the same way it is trimmed. Any
// rejection here must be treated as auth.ErrUnauthenticated without ever
// calling the Authenticator.
func extractBearerToken(r *http.Request) (token string, ok bool) {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 {
		return "", false
	}
	if !strings.EqualFold(fields[0], bearerScheme) {
		return "", false
	}
	return fields[1], true
}

// Authenticate returns a middleware that extracts a bearer token from the
// request's "Authorization" header, authenticates it via a, and stores the
// resulting Principal in the request context (auth.WithPrincipal), so a
// later handler or middleware (e.g. RequirePermission) can read it via
// auth.FromContext.
//
// Extraction (see extractBearerToken) happens before a.Authenticate is ever
// called: a missing header, an empty header, a scheme other than "Bearer"
// (checked case-insensitively per RFC 6750), or a "Bearer" scheme with no
// token after it is rejected as auth.ErrUnauthenticated without invoking the
// Authenticator at all, since there is no credential yet worth asking an
// identity provider about.
//
// Fail-closed: on any error from extraction or from a.Authenticate, this
// middleware writes an error response and returns without calling
// next.ServeHTTP.
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
			token, ok := extractBearerToken(r)
			if !ok {
				logger.ErrorContext(r.Context(), "authentication failed", "error", auth.ErrUnauthenticated)
				response.Error(w, r, http.StatusUnauthorized, response.CodeUnauthorized, msgs.Unauthorized)
				return
			}

			principal, err := a.Authenticate(r.Context(), token)
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
