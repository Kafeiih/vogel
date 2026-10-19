package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kafeiih/vogel/access"
	audithttpx "github.com/kafeiih/vogel/audit/httpx"
	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
	vmw "github.com/kafeiih/vogel/httpx/middleware"
	"github.com/kafeiih/vogel/httpx/response"
)

// NewRouter builds the chi mux for this example: the middleware stack, then
// every route, grouped by whether it requires authentication.
func NewRouter(
	docs *DocumentHandler,
	auditHandler *audithttpx.Handler,
	authenticator auth.Authenticator,
	checker authz.Checker,
	guard *access.Guard,
	metrics *vmw.Metrics,
	limiter *ipRateLimiter,
	bucket http.Handler,
	pool *pgxpool.Pool,
	logger *slog.Logger,
) http.Handler {
	r := chi.NewRouter()

	// vogel's request-ID middleware MUST run upstream of vmw.RequestContext:
	// RequestContext does not generate an ID itself. Without vmw.RequestID
	// mounted first, every downstream reader of reqctx
	// (logger.Logger.WithContext, audit.Recorder.Record) would silently see
	// an empty request ID. Unlike chi's middleware.RequestID, it always
	// generates the ID server-side and never adopts a client X-Request-Id.
	r.Use(vmw.RequestID)

	// The sole writer of reqctx's request ID, client IP, and User-Agent --
	// mounted immediately after vmw.RequestID so every middleware below it,
	// and every handler, can read that metadata via reqctx.
	r.Use(vmw.RequestContext)

	// Recovers a panicking handler into a 500 instead of crashing the whole
	// process; mounted early so it also protects the middleware below it.
	r.Use(vmw.Recovery(logger))

	// Sets common security headers on every response, success or error.
	r.Use(vmw.SecurityHeaders)

	// Records Prometheus request/latency/size metrics for every route,
	// including ones that later fail authentication or authorization.
	r.Use(metrics.Middleware)

	// Structured access log. Mounted last among these so its "status" and
	// "bytes" fields reflect what every earlier middleware actually did to
	// the response.
	r.Use(vmw.StructuredLogger(logger))

	// Public and unauthenticated: it answers only "ok" or "database
	// unavailable", one ping, nothing else. /metrics is NOT here: it is
	// served by newMetricsServer on its own internal listener.
	r.Get("/healthz", healthzHandler(pool))

	// The fake bucket (fakeStorage.Handler) stands in for the object store's
	// own endpoint, which is why it sits outside /api/v1 with no
	// Authenticate or rate limit: a client reaches it with a presigned URL,
	// and that URL's signature is the only credential it checks. Only GET
	// (download) and PUT (upload) exist. With storage/s3 these two lines go
	// away: the presigned URLs point at the bucket itself. The access log
	// above records the path only, never the signed query string.
	r.Get(fakeStoragePath+"*", bucket.ServeHTTP)
	r.Put(fakeStoragePath+"*", bucket.ServeHTTP)

	requireRead := vmw.RequirePermission(checker, "documents:document", "read", logger)
	requireWrite := vmw.RequirePermission(checker, "documents:document", "write", logger)
	requireAuditRead := vmw.RequirePermission(checker, "audit:entry", "read", logger)

	// requireDelete goes through the same guard DocumentHandler.Delete uses
	// for its own, later, per-instance check -- see that method's doc
	// comment for why the two-layer check exists at all instead of just one
	// or the other.
	requireDelete := vmw.RequireAccess(guard, "documents:document", "delete", logger)

	r.Route("/api/v1", func(r chi.Router) {
		// Per-client-IP budget, checked BEFORE Authenticate so a client
		// guessing tokens is throttled too, and after StructuredLogger and
		// metrics so every 429 is logged and counted. /healthz stays outside
		// it: an orchestrator probing from one address must never be told
		// to back off. See ipRateLimiter for why this is only a sample.
		r.Use(limiter.Middleware)
		r.Use(vmw.Authenticate(authenticator, logger))

		r.With(requireWrite).Post("/documents", docs.Create)
		r.With(requireRead).Get("/documents", docs.List)
		r.With(requireRead).Get("/documents/{id}", docs.Get)
		r.With(requireRead).Get("/documents/{id}/history", docs.History)
		r.With(requireRead).Get("/documents/{id}/download-url", docs.DownloadURL)
		// The presigned upload flow: sign a PUT, then confirm the key once
		// the client uploaded to it (see UploadURL and AttachFile). The
		// file's bytes never come through this router.
		r.With(requireWrite).Post("/documents/{id}/upload-url", docs.UploadURL)
		r.With(requireWrite).Post("/documents/{id}/file", docs.AttachFile)
		r.With(requireWrite).Post("/documents/{id}/transitions", docs.Transition)
		r.With(requireWrite).Post("/documents/{id}/claim", docs.Claim)
		r.With(requireDelete).Delete("/documents/{id}", docs.Delete)
		r.With(requireRead).Get("/inbox", docs.Inbox)

		// RequirePermission reads chi.URLParam(r, "id") for the resource ID
		// and falls back to "*" (wildcard) when the route carries none --
		// true for GET /audit (a list route) but not for GET /audit/{id}.
		r.With(requireAuditRead).Get("/audit", auditHandler.List)
		r.With(requireAuditRead).Get("/audit/{id}", auditHandler.GetByID)
	})

	return r
}

// newMetricsServer serves only /metrics, from reg, on addr (METRICS_ADDR).
// It is a separate listener from the public API so Prometheus data -- route
// names, request rates, pool sizes -- is reachable only from wherever addr
// is bound (loopback by default), never through the API port. Timeouts are
// explicit for the same reason as the API server's (gosec G112).
func newMetricsServer(addr string, reg *prometheus.Registry) *http.Server {
	mux := http.NewServeMux()
	// OpenMetrics carries the exemplars the classic text format cannot.
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{EnableOpenMetrics: true}))
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// healthzHandler pings the pool so /healthz reports the database as part of
// this process's health, not just whether the HTTP server is accepting
// connections.
func healthzHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			response.Error(w, r, http.StatusServiceUnavailable, response.CodeServiceUnavailable, "database unavailable")
			return
		}
		response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
