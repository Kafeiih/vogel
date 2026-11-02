// Package httpx is the HTTP layer of vogel/idempotency: a middleware that
// makes any mutating handler safe to retry with an Idempotency-Key header.
//
// The middleware reserves the key with a lease, runs the handler, and stores
// its response so a retry with the same key gets that response back
// (Idempotent-Replayed: true) instead of running the handler again. The
// reservation, the handler's own writes and the stored response are separate
// statements: if the process dies after the handler committed but before the
// response was stored, a retry once the lease passes runs the handler again.
// A route that must run exactly once uses idempotency/postgres.Store.Do
// inside its own transaction instead.
package httpx

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/idempotency"
	"github.com/kafeiih/vogel/pgxtx"
)

const (
	// HeaderKey is the request header carrying the client's key.
	HeaderKey = "Idempotency-Key"
	// HeaderReplayed is set to "true" on a response replayed from the store.
	HeaderReplayed = "Idempotent-Replayed"

	// DefaultLease is how long a reservation blocks duplicates while its
	// handler runs, unless Config.Lease sets another duration.
	DefaultLease = time.Minute
	// DefaultMaxRequestBytes bounds the body read to fingerprint a request.
	DefaultMaxRequestBytes = 1 << 20
	// DefaultMaxResponseBytes bounds the response body stored for replay.
	DefaultMaxResponseBytes = 1 << 20

	// storeTimeout bounds each store call made after the handler returned,
	// on a context detached from the request's so a client that went away
	// does not leave the reservation behind.
	storeTimeout = 5 * time.Second
)

// Store is the part of idempotency/postgres.Store the middleware uses.
type Store interface {
	Reserve(ctx context.Context, db pgxtx.DBTX, req idempotency.Request, lease time.Duration) (idempotency.Reservation, error)
	Complete(ctx context.Context, db pgxtx.DBTX, req idempotency.Request, token uuid.UUID, resp idempotency.Response) error
	Release(ctx context.Context, db pgxtx.DBTX, req idempotency.Request, token uuid.UUID) error
}

// Config configures Middleware.
type Config struct {
	// Store and DB are where keys are reserved and responses stored,
	// normally an idempotency/postgres.Store and the pool.
	Store Store
	DB    pgxtx.DBTX
	// Lease is how long a reservation blocks a duplicate (409) while the
	// handler runs. It must outlast the handler: a retry after it passes,
	// without a stored response, runs the handler again. Zero means
	// DefaultLease.
	Lease time.Duration
	// Required rejects a request without the header (400). Without it, such
	// a request runs as if the middleware were absent.
	Required bool
	// MaxRequestBytes bounds the body read to fingerprint the request (413
	// beyond it). Zero means DefaultMaxRequestBytes.
	MaxRequestBytes int64
	// MaxResponseBytes bounds the response body stored for replay. A larger
	// response is still sent to the client, but not stored: its reservation
	// is released, so a retry runs the handler again. Zero means
	// DefaultMaxResponseBytes.
	MaxResponseBytes int
	// Logger reports store failures after the handler ran. Nil means
	// slog.Default().
	Logger *slog.Logger
}

// Middleware returns an HTTP middleware that applies Idempotency-Key to the
// requests it wraps; mount it on mutating routes, after authentication.
//
// The key is scoped to the authenticated principal, the method and the
// request path (see Scope), and fingerprinted with the method, the path, the query and
// the body, which the middleware reads (bounded) and restores for the
// handler. Then:
//
//   - no principal: the request passes through untouched;
//   - no header: 400 VALIDATION_ERROR when Config.Required, else pass
//     through; an invalid key (empty, too long, not printable ASCII) is 400;
//   - first request: the handler runs; a response below 500 is stored with
//     the headers the handler set (not those of outer middleware, such as
//     X-Request-ID, and never Set-Cookie), a 5xx or a panic releases the key;
//   - the key's response is stored: it is replayed with
//     Idempotent-Replayed: true, without running the handler;
//   - the key's first request is still running: 409 CONFLICT;
//   - the key was used with another method, path, query or body: 422
//     UNPROCESSABLE_ENTITY;
//   - the store fails before the handler runs: 503 SERVICE_UNAVAILABLE.
func Middleware(cfg Config) func(http.Handler) http.Handler {
	if cfg.Lease <= 0 {
		cfg.Lease = DefaultLease
	}
	if cfg.MaxRequestBytes <= 0 {
		cfg.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serve(cfg, next, w, r)
		})
	}
}

func serve(cfg Config, next http.Handler, w http.ResponseWriter, r *http.Request) {
	p := auth.FromContext(r.Context())
	if p == nil || p.UserID == "" {
		next.ServeHTTP(w, r)
		return
	}
	key := r.Header.Get(HeaderKey)
	if key == "" {
		if cfg.Required {
			response.Error(w, r, http.StatusBadRequest, response.CodeValidationError, "The Idempotency-Key header is required")
			return
		}
		next.ServeHTTP(w, r)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cfg.MaxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			response.Error(w, r, http.StatusRequestEntityTooLarge, response.CodeBodyTooLarge, "Request body too large")
			return
		}
		response.Error(w, r, http.StatusBadRequest, response.CodeValidationError, "Could not read the request body")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	req := idempotency.Request{
		Scope:       Scope(p.UserID, r.Method, r.URL.Path),
		Key:         key,
		Fingerprint: idempotency.Fingerprint([]byte(r.Method), []byte(r.URL.Path), []byte(r.URL.RawQuery), body),
	}
	if err := req.Validate(); err != nil {
		response.Error(w, r, http.StatusBadRequest, response.CodeValidationError, "Invalid Idempotency-Key header")
		return
	}

	res, err := cfg.Store.Reserve(r.Context(), cfg.DB, req, cfg.Lease)
	switch {
	case errors.Is(err, idempotency.ErrInProgress):
		response.Error(w, r, http.StatusConflict, response.CodeConflict, "A request with this Idempotency-Key is still in progress")
		return
	case errors.Is(err, idempotency.ErrKeyReused):
		response.Error(w, r, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity, "This Idempotency-Key was used with a different request")
		return
	case err != nil:
		cfg.Logger.ErrorContext(r.Context(), "idempotency: reserve key", "error", err)
		response.Error(w, r, http.StatusServiceUnavailable, response.CodeServiceUnavailable, "Service temporarily unavailable")
		return
	}
	if res.Replay != nil {
		replay(w, *res.Replay)
		return
	}

	before := w.Header().Clone()
	rec := &recorder{ResponseWriter: w, max: cfg.MaxResponseBytes}
	defer func() {
		if v := recover(); v != nil {
			release(cfg, r, req, res.Token)
			panic(v)
		}
	}()
	next.ServeHTTP(rec, r)

	status, header := rec.result()
	if status >= http.StatusInternalServerError || rec.overflow {
		release(cfg, r, req, res.Token)
		return
	}
	header = handlerHeaders(before, header)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), storeTimeout)
	defer cancel()
	if err := cfg.Store.Complete(ctx, cfg.DB, req, res.Token, idempotency.Response{
		Status: status, Header: header, Body: rec.body.Bytes(),
	}); err != nil {
		cfg.Logger.ErrorContext(r.Context(), "idempotency: store response", "error", err)
	}
}

// release frees req's reservation so a retry runs the handler again.
func release(cfg Config, r *http.Request, req idempotency.Request, token uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), storeTimeout)
	defer cancel()
	if err := cfg.Store.Release(ctx, cfg.DB, req, token); err != nil {
		cfg.Logger.ErrorContext(r.Context(), "idempotency: release key", "error", err)
	}
}

// replay writes a stored response.
func replay(w http.ResponseWriter, resp idempotency.Response) {
	for name, values := range resp.Header {
		w.Header()[name] = append([]string(nil), values...)
	}
	w.Header().Set(HeaderReplayed, "true")
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// recorder passes the handler's response through to the client and keeps a
// copy of its status, headers and body (up to max bytes) for the store.
type recorder struct {
	http.ResponseWriter
	max      int
	status   int
	header   http.Header
	body     bytes.Buffer
	overflow bool
}

func (rec *recorder) WriteHeader(code int) {
	if rec.status == 0 && code >= http.StatusOK {
		rec.status = code
		rec.header = rec.ResponseWriter.Header().Clone()
	}
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *recorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.WriteHeader(http.StatusOK)
	}
	if !rec.overflow {
		if rec.body.Len()+len(b) > rec.max {
			rec.overflow = true
			rec.body.Reset()
		} else {
			rec.body.Write(b)
		}
	}
	return rec.ResponseWriter.Write(b)
}

// Flush sends what was written so far, keeping the recorder an
// http.Flusher for handlers that type-assert it.
func (rec *recorder) Flush() {
	if rec.status == 0 {
		rec.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(rec.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (rec *recorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// result returns the status and headers the handler sent: 200 and the
// current headers when it never wrote anything.
func (rec *recorder) result() (int, http.Header) {
	if rec.status == 0 {
		return http.StatusOK, rec.ResponseWriter.Header().Clone()
	}
	return rec.status, rec.header
}

// Scope is the idempotency.Request.Scope the middleware uses for a request:
// "http:" and a hash of the principal, the method and the path, each
// length-prefixed. Hashing keeps two different (user, method, path) tuples
// from ever sharing a scope (a space in a user ID or a path cannot shift the
// boundaries), bounds the scope whatever the path length, and the prefix
// keeps middleware scopes apart from those a service passes to Store.Do.
func Scope(user, method, path string) string {
	return "http:" + hex.EncodeToString(idempotency.Fingerprint([]byte(user), []byte(method), []byte(path)))
}

// handlerHeaders returns the headers of final the handler set or changed,
// compared with before (the headers outer middleware had already set, such
// as X-Request-ID): only those belong to the stored response. Set-Cookie, in
// any spelling, and HeaderReplayed are never stored.
func handlerHeaders(before, final http.Header) http.Header {
	out := http.Header{}
	for name, values := range final {
		if strings.EqualFold(name, "Set-Cookie") || strings.EqualFold(name, HeaderReplayed) {
			continue
		}
		if prev, ok := before[name]; ok && slices.Equal(prev, values) {
			continue
		}
		out[name] = append([]string(nil), values...)
	}
	return out
}
