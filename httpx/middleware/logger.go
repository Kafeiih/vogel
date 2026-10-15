package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/kafeiih/vogel/reqctx"
)

// StructuredLogger returns a structured access-log middleware.
//
// The request ID comes from reqctx, which RequestContext populates — mount
// RequestContext upstream of this middleware. Reading chi's GetReqID directly
// here instead would agree with what logger.Logger.WithContext and
// audit.Recorder.Record report only by coincidence, since both of those read
// reqctx: a request ID reaching the context by any other route would land in
// an audit_log row while this access log printed an empty one.
//
// When the client sent its own correlation ID (see RequestID), it is logged
// under "client_request_id", apart from the server-generated "request_id",
// so a client-chosen value is never mistaken for the server's; the key is
// left out when there is none.
//
// The authenticated user's ID is logged under "user_id", read from the
// reqctx user slot that RequestContext installs and Authenticate fills on
// success. A slot is needed because this middleware wraps Authenticate: the
// Principal Authenticate stores lives on a derived request this middleware
// never sees, while the slot is a pointer shared by both. The slot is read in
// the deferred log line, after next has returned, so the write has already
// happened on the same goroutine. The key is left out for an unauthenticated
// request or when RequestContext is not mounted.
func StructuredLogger(logger *slog.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			defer func() {
				attrs := []any{
					"method", r.Method,
					"path", r.URL.Path,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"duration_ms", time.Since(start).Milliseconds(),
					"remote_addr", r.RemoteAddr,
					"user_agent", r.UserAgent(),
					"request_id", reqctx.RequestIDFromContext(r.Context()),
				}
				if clientID := reqctx.ClientRequestIDFromContext(r.Context()); clientID != "" {
					attrs = append(attrs, "client_request_id", clientID)
				}
				if userID := reqctx.UserIDFromContext(r.Context()); userID != "" {
					attrs = append(attrs, "user_id", userID)
				}
				logger.Info("http request", attrs...)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}
