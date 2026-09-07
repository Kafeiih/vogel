package http

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/rs/cors"
	httpSwagger "github.com/swaggo/http-swagger/v2"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
	vmw "github.com/kafeiih/vogel/httpx/middleware"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/handler"
)

// clientIPKey is an httprate.KeyFunc that rate-limits by client IP.
//
// httprate.KeyByIP is deprecated in favor of resolving the client IP
// upstream (e.g. via chi's middleware.ClientIPFromXFF) and keying off that —
// but chi's own middleware.RealIP is already mounted upstream in NewRouter
// and rewrites r.RemoteAddr from the trusted proxy headers this application
// configures, so reading r.RemoteAddr here is equivalent without adding a
// second, redundant IP-resolution middleware. CanonicalizeIP buckets IPv6
// clients by their /64 so one client cannot rotate within its own block to
// dodge the limit.
func clientIPKey(r *http.Request) (string, error) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	return httprate.CanonicalizeIP(ip), nil
}

// RouterConfig groups the configuration values that the router needs.
type RouterConfig struct {
	AppURL      string
	Timeout     time.Duration
	CORSOrigins []string
}

// AuthMessages holds the Spanish user-facing strings for authentication and
// authorization failures. This boilerplate is for Spanish-speaking apps, so
// it always sets these explicitly instead of falling back to vmw's neutral
// English defaults — see vmw.WithAuthMessages.
var authMessages = vmw.WithAuthMessages(vmw.AuthMessages{
	Unauthorized:       "Autenticación requerida",
	Forbidden:          "No tenés permisos para realizar esta acción",
	ServiceUnavailable: "Servicio no disponible temporalmente",
})

// Dependencies contains all dependencies required by the router.
type Dependencies struct {
	Config        RouterConfig
	Authenticator auth.Authenticator
	AuthzChecker  authz.Checker
	Metrics       *vmw.Metrics
	Logger        *slog.Logger
	HealthHandler *handler.HealthHandler
	AuditHandler  *handler.AuditHandler
}

func NewRouter(deps Dependencies) *chi.Mux {
	r := chi.NewRouter()

	// chi's own request-ID middleware MUST run upstream of
	// vmw.RequestContext: RequestContext reads the request ID via chi's
	// middleware.GetReqID, it does not generate one itself.
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)

	// The sole writer of reqctx's request ID, client IP, and User-Agent.
	// Mounted immediately after RequestID and before StructuredLogger: if
	// this were mounted any later, log lines and audit_log rows would lose
	// the request ID silently (see vogel's MIGRATION_GUIDE.md, section
	// "reqctx").
	r.Use(vmw.RequestContext)

	r.Use(vmw.StructuredLogger(deps.Logger))
	r.Use(vmw.Recovery(deps.Logger))
	r.Use(deps.Metrics.Middleware)
	r.Use(corsMiddleware(deps.Config.CORSOrigins))

	// Rate limiting por IP: 100 req/min (rutas públicas y no autenticadas)
	r.Use(httprate.LimitBy(100, 1*time.Minute, clientIPKey,
		httprate.WithLimitHandler(vmw.RateLimitJSON()),
	))

	r.Use(middleware.Timeout(deps.Config.Timeout))

	r.Route("/v1", func(r chi.Router) {
		docsURL := "/v1/swagger/doc.json"
		r.Get("/swagger/*", httpSwagger.Handler(
			httpSwagger.URL(docsURL),
		))

		// All API routes get security headers
		r.Group(func(r chi.Router) {
			r.Use(vmw.SecurityHeaders)

			// Rutas públicas — health checks (Kubernetes probes)
			r.Get("/healthz", deps.HealthHandler.Check)
			r.Get("/readyz", deps.HealthHandler.Ready)
			r.Get("/livez", deps.HealthHandler.Live)

			// Authenticated routes
			r.Group(func(r chi.Router) {
				r.Use(vmw.Authenticate(deps.Authenticator, deps.Logger, authMessages))

				// Rate limiting por usuario autenticado: 1000 req/min
				r.Use(httprate.LimitBy(1000, 1*time.Minute,
					func(r *http.Request) (string, error) {
						if principal := auth.FromContext(r.Context()); principal != nil {
							return principal.UserID, nil
						}
						return clientIPKey(r)
					},
					httprate.WithLimitHandler(vmw.RateLimitJSON()),
				))

				// Audit log (read-only) — limite más estricto: 30 req/min
				r.Route("/audit", func(r chi.Router) {
					r.Use(httprate.LimitBy(30, 1*time.Minute,
						func(r *http.Request) (string, error) {
							if principal := auth.FromContext(r.Context()); principal != nil {
								return "audit:" + principal.UserID, nil
							}
							return clientIPKey(r)
						},
						httprate.WithLimitHandler(vmw.RateLimitJSON()),
					))

					// Cerbos authorization: principal must have "list" on audit resource
					r.With(vmw.RequirePermission(deps.AuthzChecker, "audit:entry", "list", deps.Logger, authMessages)).
						Get("/", deps.AuditHandler.List)
					r.With(vmw.RequirePermission(deps.AuthzChecker, "audit:entry", "view", deps.Logger, authMessages)).
						Get("/{id}", deps.AuditHandler.GetByID)
				})
			})
		})
	})

	return r
}

// corsMiddleware configura CORS usando los orígenes de la configuración.
// Si no hay orígenes configurados, usa localhost como fallback seguro.
func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	if len(allowedOrigins) == 0 {
		allowedOrigins = []string{"http://localhost:3000"}
	}
	c := cors.New(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	})
	return c.Handler
}
