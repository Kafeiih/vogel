// Package reqctx carries request-scoped metadata — a request/correlation ID,
// the client IP, and the User-Agent — through a context.Context, independent
// of any HTTP router or logging framework. It depends on nothing beyond the
// standard library's context package.
//
// reqctx exists as neutral ground between two layers that must never import
// each other:
//
//   - httpx/middleware (the transport layer) legitimately knows about the
//     inbound *http.Request and chi's request-ID middleware, and is the
//     writer: it populates a request ID, IP, and User-Agent here for every
//     inbound request.
//   - logger and audit (application-facing layers) read this metadata
//     without depending on httpx or chi. logger stamps every log line with
//     the request ID; audit stamps every audit_log row with it. Because both
//     read the exact same context key owned by this package, the request ID
//     that reaches a log line and the one that reaches an audit row are
//     provably the same value — see audit.Recorder.Record.
package reqctx

import "context"

// requestIDKey is the context key this package owns for storing a
// request/correlation ID. It is unexported so only WithRequestID may set it.
type requestIDKey struct{}

// requestInfoKey is the context key this package owns for storing RequestInfo.
// It is unexported so only WithRequestInfo may set it — no context key is
// exported, so tests inject metadata via WithRequestInfo instead of reaching
// into the key directly.
type requestInfoKey struct{}

// RequestInfo holds the client IP and User-Agent extracted from an inbound
// HTTP request.
type RequestInfo struct {
	IP        string
	UserAgent string
}

// WithRequestID returns a copy of ctx carrying id as the request/correlation
// ID. Callers that bridge a specific transport's request ID (e.g. an HTTP
// middleware reading chi's request ID) call this to make the ID available to
// logger.Logger.WithContext and audit.Recorder.Record without either of
// those packages depending on that transport.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext returns the request/correlation ID previously stored
// via WithRequestID, or "" if none is present.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// WithRequestInfo returns a copy of ctx carrying info as the request's client
// IP and User-Agent.
//
// This is the only way to inject RequestInfo into a context: no context key
// is exported. Production code populates it via
// httpx/middleware.RequestContext; tests in any package call this directly
// instead of reaching into a leaky exported key.
func WithRequestInfo(ctx context.Context, info RequestInfo) context.Context {
	return context.WithValue(ctx, requestInfoKey{}, info)
}

// RequestInfoFromContext returns the RequestInfo previously stored via
// WithRequestInfo, and whether one was present.
func RequestInfoFromContext(ctx context.Context) (RequestInfo, bool) {
	info, ok := ctx.Value(requestInfoKey{}).(RequestInfo)
	return info, ok
}
