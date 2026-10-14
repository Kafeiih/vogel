package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/kafeiih/vogel/reqctx"
)

// RequestIDHeader is the header RequestID writes the server request ID to,
// and the header a client's own correlation ID is read from.
const RequestIDHeader = "X-Request-Id"

// maxClientRequestIDLen bounds the sanitized client correlation ID kept in
// reqctx, so a client cannot inflate every log line it causes.
const maxClientRequestIDLen = 128

// RequestID assigns every request a fresh server-generated ID: 16 bytes from
// crypto/rand, hex-encoded (32 lowercase hex characters). It stores the ID in
// reqctx (read by RequestContext, logger and audit) and under chi's
// middleware.RequestIDKey (read by chi's GetReqID, e.g. the trace_id of
// httpx/response errors), and sets it on the response's X-Request-Id header.
//
// Unlike chi's middleware.RequestID, it never adopts an incoming
// X-Request-Id: a client could otherwise choose the request_id stamped on
// logs and audit rows, collide with another request's ID, or inject log
// content. A client-supplied value is instead kept, for correlation only,
// via reqctx.WithClientRequestID, after stripping every byte outside visible
// ASCII (0x21-0x7E, so no spaces or control characters) and truncating it to
// 128 bytes; an empty result is not stored.
//
// If crypto/rand fails, RequestID panics: rand.Read never returns an error on
// supported platforms (see its documentation), and an unidentifiable request
// is not a state worth limping along in.
//
// Mount it first, upstream of RequestContext and every middleware that logs.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()

		ctx := reqctx.WithRequestID(r.Context(), id)
		ctx = context.WithValue(ctx, chimw.RequestIDKey, id)
		if clientID := sanitizeClientRequestID(r.Header.Get(RequestIDHeader)); clientID != "" {
			ctx = reqctx.WithClientRequestID(ctx, clientID)
		}

		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newRequestID returns 16 random bytes from crypto/rand as 32 hex characters.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("httpx/middleware: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// sanitizeClientRequestID keeps only visible ASCII bytes (0x21-0x7E) of s,
// up to maxClientRequestIDLen of them.
func sanitizeClientRequestID(s string) string {
	out := make([]byte, 0, min(len(s), maxClientRequestIDLen))
	for i := 0; i < len(s) && len(out) < maxClientRequestIDLen; i++ {
		if c := s[i]; c >= 0x21 && c <= 0x7e {
			out = append(out, c)
		}
	}
	return string(out)
}
