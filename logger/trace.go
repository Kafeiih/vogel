package logger

import "context"

// TraceIDsFunc returns the trace and span IDs of the span carried by ctx,
// or empty strings when there is none. It is the port tracing plugs into:
// vogel/otel's TraceIDs is one for OpenTelemetry, and logger itself
// depends on no tracing library.
type TraceIDsFunc func(ctx context.Context) (traceID, spanID string)

// WithTraceIDs adds trace_id and span_id, read with fn, to every record
// logged with a context (InfoContext and the other *Context methods, and
// package-level slog calls once the Logger is the default), and to the
// attributes WithContext binds. A key is added only when fn returns a
// non-empty value and the record or logger does not already carry it, the
// same rules as request_id. A nil fn leaves the Logger as without the option.
func WithTraceIDs(fn TraceIDsFunc) Option {
	return func(c *config) { c.traceIDs = fn }
}
