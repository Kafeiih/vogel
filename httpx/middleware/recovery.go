package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/internal/logtext"
)

// maxLoggedPanicBytes bounds the panic value Recovery logs. A panic value can
// be anything a handler held, including request data, so a longer value is
// cut at a rune boundary and flagged with error_truncated.
const maxLoggedPanicBytes = 1024

// Recovery recovers from panics in the handler chain and returns a 500 error.
//
// The log line carries the panic value's Go type under "panic_type", the
// path, the method and the stack trace. "error" holds the value as text,
// bounded by maxLoggedPanicBytes, only when the value is an error (its
// Error() text), a string, or a bool or number: for any other value, such as
// a struct, fmt would print its unexported fields too, so only the type is
// logged.
func Recovery(logger *slog.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					// Log the panic with its stack trace.
					attrs := []any{"panic_type", fmt.Sprintf("%T", err)}
					if text, ok := panicText(err); ok {
						value, truncated := logtext.Truncate(text, maxLoggedPanicBytes)
						attrs = append(attrs, "error", value)
						if truncated {
							attrs = append(attrs, "error_truncated", true)
						}
					}
					attrs = append(attrs,
						"path", r.URL.Path,
						"method", r.Method,
						"stack", string(debug.Stack()),
					)
					// ErrorContext lets a context-aware handler (logger.New's)
					// add the request_id and user_id of the request.
					logger.ErrorContext(r.Context(), "panic recovered", attrs...)

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

// panicText returns the text Recovery logs for a panic value v, and false
// when v is of a kind whose text is not logged (see Recovery). fmt.Sprint
// recovers a panic raised inside an Error method, so a typed-nil error
// cannot crash the recovery path.
func panicText(v any) (string, bool) {
	switch v.(type) {
	case error, string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, uintptr,
		float32, float64:
		return fmt.Sprint(v), true
	default:
		return "", false
	}
}
