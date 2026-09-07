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
func StructuredLogger(logger *slog.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			defer func() {
				logger.Info("http request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"duration_ms", time.Since(start).Milliseconds(),
					"remote_addr", r.RemoteAddr,
					"user_agent", r.UserAgent(),
					"request_id", reqctx.RequestIDFromContext(r.Context()),
				)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}
