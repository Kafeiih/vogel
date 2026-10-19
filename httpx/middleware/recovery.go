package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"runtime/debug"
	"strconv"

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
// Error() text) or of a string, bool or numeric kind, named types included,
// rendered from the underlying value so a String method cannot change what
// is logged: for any other value, such as a struct, fmt would print its
// unexported fields too, so only the type is logged.
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

// panicText returns the text Recovery logs for a panic value v, and false
// when v is of a kind whose text is not logged (see Recovery). fmt.Sprint
// recovers a panic raised inside an Error method, so a typed-nil error
// cannot crash the recovery path.
func panicText(v any) (string, bool) {
	if _, ok := v.(error); ok {
		return fmt.Sprint(v), true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return rv.String(), true
	case reflect.Bool:
		return strconv.FormatBool(rv.Bool()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(rv.Uint(), 10), true
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(rv.Float(), 'g', -1, rv.Type().Bits()), true
	default:
		return "", false
	}
}
