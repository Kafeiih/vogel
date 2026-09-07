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
// Mount chi's own middleware.RequestID (or equivalent) upstream of this
// middleware — RequestContext reads the upstream ID via chi's GetReqID
// rather than generating one itself.
func RequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := reqctx.WithRequestID(r.Context(), chimw.GetReqID(r.Context()))

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
