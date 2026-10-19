package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"unicode/utf8"

	"github.com/kafeiih/vogel/httpx/response"
)

// maxLoggedPanicBytes bounds the panic value Recovery logs. A panic value can
// be anything a handler held, including request data, so a longer value is
// cut at a rune boundary and flagged with error_truncated.
const maxLoggedPanicBytes = 1024

// Recovery recovers from panics in the handler chain and returns a 500 error.
//
// The log line carries the panic value under "error" (bounded by
// maxLoggedPanicBytes), its Go type under "panic_type", the path, the method
// and the stack trace.
func Recovery(logger *slog.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					// Log the panic with its stack trace.
					value, truncated := truncateUTF8(fmt.Sprint(err), maxLoggedPanicBytes)
					attrs := []any{"error", value, "panic_type", fmt.Sprintf("%T", err)}
					if truncated {
						attrs = append(attrs, "error_truncated", true)
					}
					attrs = append(attrs,
						"path", r.URL.Path,
						"method", r.Method,
						"stack", string(debug.Stack()),
					)
					logger.Error("panic recovered", attrs...)

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

// truncateUTF8 returns s cut to at most n bytes without splitting a rune, and
// whether it was cut.
func truncateUTF8(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}
