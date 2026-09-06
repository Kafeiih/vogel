package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/kafeiih/vogel/httpx/response"
)

// Recovery recovers from panics in the handler chain and returns a 500 error.
func Recovery(logger *slog.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					// Log the panic with its stack trace.
					logger.Error("panic recovered",
						"error", err,
						"path", r.URL.Path,
						"method", r.Method,
						"stack", string(debug.Stack()),
					)

					// Return a 500 to the client.
					response.Error(w, r, http.StatusInternalServerError,
						"INTERNAL_ERROR",
						"Internal server error",
					)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}
