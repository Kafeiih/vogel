package middleware

import (
	"net"
	"net/http"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/kafeiih/vogel/reqctx"
)

// RequestContext populates vogel/reqctx with the three pieces of
// request-scoped metadata every downstream layer needs: the request ID, the
// client IP, and the User-Agent.
//
// It is the single writer for this metadata (replacing what used to be two
// separate middlewares — one bridging chi's request ID into logger's own
// context key, one extracting IP/User-Agent into a middleware-local key).
// Consolidating into one middleware over one neutral package (reqctx) means:
//   - logger.Logger.WithContext and audit.Recorder.Record read the exact same
//     request ID, from the exact same context key, so a log line and an
//     audit_log row for the same request are provably linked.
//   - neither logger nor audit needs to import httpx or chi to get at it.
//
// Mount vogel's RequestID upstream of this middleware: RequestContext does
// not generate an ID itself. It prefers the ID RequestID stored in reqctx
// and, only when there is none, falls back to chi's middleware.GetReqID, so
// a consumer that still mounts chi's middleware.RequestID keeps working
// unchanged. Prefer vogel's RequestID: chi's honors a client-supplied
// X-Request-Id header verbatim, so with it a client chooses the request_id
// that reaches logs, audit_log rows and error responses.
func RequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := reqctx.RequestIDFromContext(r.Context())
		if id == "" {
			id = chimw.GetReqID(r.Context())
		}
		ctx := reqctx.WithRequestID(r.Context(), id)

		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || ip == "" {
			ip = r.RemoteAddr
		}
		ctx = reqctx.WithRequestInfo(ctx, reqctx.RequestInfo{
			IP:        ip,
			UserAgent: r.UserAgent(),
		})

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
