// Package reqctx carries request-scoped metadata — a request/correlation ID,
// the client IP, the User-Agent, and the authenticated user's ID — through a
// context.Context, independent of any HTTP router, logging framework or
// identity provider. It depends on nothing beyond the standard library.
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

import (
	"context"
	"sync/atomic"
)

// requestIDKey is the context key this package owns for storing a
// request/correlation ID. It is unexported so only WithRequestID may set it.
type requestIDKey struct{}

// clientRequestIDKey is the context key for the client-supplied correlation
// ID. It is separate from requestIDKey so a client value can never be read
// back as the server request ID.
type clientRequestIDKey struct{}

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

// WithClientRequestID returns a copy of ctx carrying id as the correlation ID
// the client sent (e.g. its X-Request-Id header). It is untrusted input:
// httpx/middleware.RequestID sanitizes and length-limits it before storing
// it here, and it never replaces the server-generated request ID returned by
// RequestIDFromContext.
func WithClientRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, clientRequestIDKey{}, id)
}

// ClientRequestIDFromContext returns the client-supplied correlation ID
// previously stored via WithClientRequestID, or "" if none is present.
func ClientRequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(clientRequestIDKey{}).(string)
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

// userSlotKey is the context key for the per-request user slot. Unexported so
// only WithUserSlot may install one.
type userSlotKey struct{}

// userSlot is a mutable, per-request cell holding the authenticated user's
// ID.
//
// It is mutable — unlike every other value in this package — because of where
// the writer and the reader sit in an HTTP middleware chain. The access log
// (httpx/middleware.StructuredLogger) wraps the authentication middleware
// (httpx/middleware.Authenticate), and Authenticate can only hand the
// Principal downstream, on a derived request created with r.WithContext; a
// value stored there is invisible to the outer access logger, which still
// holds the ancestor context. A pointer installed upstream of both
// (httpx/middleware.RequestContext) is shared by every derived context, so a
// write through the inner context is visible through the outer one.
//
// The ID is held in an atomic.Pointer rather than a plain field. In the
// ordinary chain the write (Authenticate, before calling next) and the read
// (StructuredLogger's deferred log line, after next returns) happen on the
// same goroutine, and a plain field would be enough. But a handler wrapped in
// http.TimeoutHandler runs on a separate goroutine that keeps going after the
// timeout response is written: an Authenticate inside it, still waiting on a
// slow identity provider, can then write the slot while the access logger
// outside it is reading. The atomic makes that interleaving well-defined
// (the log line carries either the ID or nothing) instead of a data race.
type userSlot struct {
	userID atomic.Pointer[string]
}

// WithUserSlot returns a copy of ctx carrying an empty user slot that
// SetUserID can fill later through any context derived from it. If ctx
// already carries a slot, ctx is returned unchanged: a second, inner slot
// would shadow the first, and a middleware holding the outer context would
// never see a user ID written through the inner one.
//
// Production code installs the slot via httpx/middleware.RequestContext.
func WithUserSlot(ctx context.Context) context.Context {
	if _, ok := ctx.Value(userSlotKey{}).(*userSlot); ok {
		return ctx
	}
	return context.WithValue(ctx, userSlotKey{}, &userSlot{})
}

// SetUserID records id as the authenticated user's ID in the slot ctx
// carries. It is a no-op when ctx carries no slot (WithUserSlot was never
// called upstream), so an authentication middleware mounted without
// httpx/middleware.RequestContext keeps working and the ID is simply not
// logged.
//
// Only an opaque, stable user identifier belongs here — never a username,
// roles, or a credential: the value ends up in every access-log line.
func SetUserID(ctx context.Context, id string) {
	if slot, ok := ctx.Value(userSlotKey{}).(*userSlot); ok {
		slot.userID.Store(&id)
	}
}

// UserIDFromContext returns the user ID previously recorded via SetUserID, or
// "" if ctx carries no slot or the slot was never filled (an unauthenticated
// request).
func UserIDFromContext(ctx context.Context) string {
	slot, ok := ctx.Value(userSlotKey{}).(*userSlot)
	if !ok {
		return ""
	}
	if id := slot.userID.Load(); id != nil {
		return *id
	}
	return ""
}
