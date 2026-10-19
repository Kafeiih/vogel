package middleware

import (
	"log/slog"
	"net/http"
	"net/netip"
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
//
// The client's address ("remote_addr", r.RemoteAddr) and "user_agent" are
// personal data under GDPR-style rules. Both are logged by default;
// WithRemoteAddr truncates or omits the address and WithoutUserAgent omits
// the user agent.
func StructuredLogger(logger *slog.Logger, opts ...LoggerOption) func(next http.Handler) http.Handler {
	cfg := loggerConfig{remoteAddr: RemoteAddrFull, userAgent: true}
	for _, opt := range opts {
		opt(&cfg)
	}
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
				}
				if addr, ok := cfg.remoteAddrValue(r.RemoteAddr); ok {
					attrs = append(attrs, "remote_addr", addr)
				}
				if cfg.userAgent {
					attrs = append(attrs, "user_agent", r.UserAgent())
				}
				attrs = append(attrs, "request_id", reqctx.RequestIDFromContext(r.Context()))
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

// RemoteAddrMode selects how StructuredLogger logs the client address.
type RemoteAddrMode int

const (
	// RemoteAddrFull logs r.RemoteAddr as is (the default).
	RemoteAddrFull RemoteAddrMode = iota
	// RemoteAddrTruncated logs only the network part of the address: the
	// first 24 bits of an IPv4 address and the first 48 of an IPv6 one, with
	// no port, so the line still shows where traffic comes from without
	// identifying one host. An address that does not parse is left out.
	RemoteAddrTruncated
	// RemoteAddrOmitted leaves remote_addr out of the line.
	RemoteAddrOmitted
)

const (
	truncatedIPv4Bits = 24
	truncatedIPv6Bits = 48
)

// LoggerOption configures StructuredLogger.
type LoggerOption func(*loggerConfig)

type loggerConfig struct {
	remoteAddr RemoteAddrMode
	userAgent  bool
}

// WithRemoteAddr sets how the client address is logged.
func WithRemoteAddr(mode RemoteAddrMode) LoggerOption {
	return func(c *loggerConfig) { c.remoteAddr = mode }
}

// WithoutUserAgent leaves user_agent out of the line.
func WithoutUserAgent() LoggerOption {
	return func(c *loggerConfig) { c.userAgent = false }
}

// remoteAddrValue returns the remote_addr value for raw under c, and false
// when the key must be left out.
func (c loggerConfig) remoteAddrValue(raw string) (string, bool) {
	switch c.remoteAddr {
	case RemoteAddrFull:
		return raw, true
	case RemoteAddrTruncated:
		return truncateAddr(raw)
	default:
		// RemoteAddrOmitted, and any value outside the declared modes: an
		// unknown privacy setting fails closed.
		return "", false
	}
}

// truncateAddr parses raw as "ip:port" or a bare IP (what RemoteAddr holds
// behind chi's RealIP) and returns its masked network address.
func truncateAddr(raw string) (string, bool) {
	var addr netip.Addr
	if ap, err := netip.ParseAddrPort(raw); err == nil {
		addr = ap.Addr()
	} else if a, err := netip.ParseAddr(raw); err == nil {
		addr = a
	} else {
		return "", false
	}
	addr = addr.Unmap().WithZone("")
	bits := truncatedIPv6Bits
	if addr.Is4() {
		bits = truncatedIPv4Bits
	}
	prefix, err := addr.Prefix(bits)
	if err != nil {
		return "", false
	}
	return prefix.Addr().String(), true
}
