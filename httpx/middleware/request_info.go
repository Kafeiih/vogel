package middleware

import (
	"context"
	"net"
	"net/http"
)

type contextKey string

const requestInfoKey contextKey = "requestInfo"

// RequestInfo holds the client IP and User-Agent extracted from the HTTP request.
type RequestInfo struct {
	IP        string
	UserAgent string
}

// RequestInfoContextKey returns the context key used to store RequestInfo.
// Useful for injecting RequestInfo in tests.
func RequestInfoContextKey() contextKey {
	return requestInfoKey
}

// RequestInfoFromContext extracts the RequestInfo from the context, if present.
func RequestInfoFromContext(ctx context.Context) *RequestInfo {
	info, _ := ctx.Value(requestInfoKey).(*RequestInfo)
	return info
}

// RequestInfoMiddleware injects IP and User-Agent into the request context.
// It does NOT perform any auditing — it only makes request metadata available
// for downstream code (e.g. the audit Recorder).
func RequestInfoMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip == "" {
			ip = r.RemoteAddr
		}

		info := &RequestInfo{
			IP:        ip,
			UserAgent: r.UserAgent(),
		}

		ctx := context.WithValue(r.Context(), requestInfoKey, info)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
