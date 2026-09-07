package main

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

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
	metrics *vmw.Metrics,
	pool *pgxpool.Pool,
	reg *prometheus.Registry,
	logger *slog.Logger,
) http.Handler {
	r := chi.NewRouter()

	// chi's own request-ID middleware MUST run upstream of vmw.RequestContext:
	// RequestContext reads the request ID via chi's middleware.GetReqID, it
	// does not generate one itself. Without chimw.RequestID mounted first,
	// every downstream reader of reqctx (logger.Logger.WithContext,
	// audit.Recorder.Record) would silently see an empty request ID.
	r.Use(chimw.RequestID)

	// The sole writer of reqctx's request ID, client IP, and User-Agent --
	// mounted immediately after chimw.RequestID so every middleware below it,
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
	//
	// Deliberately absent: a rate limiter. httprate (and vmw's
	// RateLimitJSON, built on top of it) would be the natural fit here, but
	// github.com/go-chi/httprate is not a dependency of this module, and
	// this example must not add one just to demonstrate a middleware stack.
	r.Use(vmw.StructuredLogger(logger))

	r.Get("/healthz", healthzHandler(pool))
	r.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	requireRead := vmw.RequirePermission(checker, "documents:document", "read", logger)
	requireWrite := vmw.RequirePermission(checker, "documents:document", "write", logger)
	requireAuditRead := vmw.RequirePermission(checker, "audit:entry", "read", logger)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(vmw.Authenticate(authenticator, logger))

		r.With(requireWrite).Post("/documents", docs.Create)
		r.With(requireRead).Get("/documents", docs.List)
		r.With(requireRead).Get("/documents/{id}", docs.Get)
		r.With(requireRead).Get("/documents/{id}/history", docs.History)
		r.With(requireRead).Get("/documents/{id}/download-url", docs.DownloadURL)
		r.With(requireWrite).Put("/documents/{id}/file", docs.UploadFile)
		r.With(requireWrite).Post("/documents/{id}/transitions", docs.Transition)
		r.With(requireWrite).Post("/documents/{id}/claim", docs.Claim)
		r.With(requireRead).Get("/inbox", docs.Inbox)

		// RequirePermission reads chi.URLParam(r, "id") for the resource ID
		// and falls back to "*" (wildcard) when the route carries none --
		// true for GET /audit (a list route) but not for GET /audit/{id}.
		r.With(requireAuditRead).Get("/audit", auditHandler.List)
		r.With(requireAuditRead).Get("/audit/{id}", auditHandler.GetByID)
	})

	return r
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
