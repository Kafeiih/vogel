package http

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
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
// upstream and keying off that. vmw.RealIP is mounted upstream in NewRouter
// and rewrites r.RemoteAddr from X-Forwarded-For / X-Real-IP only when the
// direct peer is one of RouterConfig.TrustedProxies (TRUSTED_PROXIES), so
// reading r.RemoteAddr here is equivalent without a second, redundant
// IP-resolution middleware -- and a client connecting directly cannot spoof
// its way into a fresh bucket. CanonicalizeIP buckets IPv6
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

	// TrustedProxies are the only peers whose X-Forwarded-For / X-Real-IP
	// headers vmw.RealIP believes (TRUSTED_PROXIES). Empty trusts none.
	TrustedProxies []netip.Prefix

	// EnableSwagger mounts the Swagger UI and spec under /v1/swagger/.
	// main.go sets it from SWAGGER_ENABLED (default false, refused in
	// production); the zero value leaves the routes unregistered, so a
	// forgotten setting fails closed.
	EnableSwagger bool
}

// swaggerCSP replaces SecurityHeaders' "default-src 'none'" on the Swagger
// routes only. swagger-ui's index.html bootstraps itself with an inline
// <script> and inline styles and uses data: images, all served from this
// origin; nothing else is loosened, and framing stays denied.
const swaggerCSP = "default-src 'self'; script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

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

	// vogel's request-ID middleware MUST run upstream of
	// vmw.RequestContext: RequestContext does not generate an ID itself.
	// Unlike chi's middleware.RequestID, it always generates the ID
	// server-side and never adopts a client-supplied X-Request-Id.
	r.Use(vmw.RequestID)

	// Resolve the client IP before RequestContext records it (reqctx,
	// audit_log.ip) and before the IP-keyed rate limiter reads it. Unlike
	// chi's middleware.RealIP, forwarding headers are only believed from a
	// configured trusted proxy; with TRUSTED_PROXIES empty they are ignored.
	r.Use(vmw.RealIP(deps.Config.TrustedProxies...))

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
		// All API routes get security headers
		r.Group(func(r chi.Router) {
			r.Use(vmw.SecurityHeaders)

			// API description: only on an explicit opt-in
			// (SWAGGER_ENABLED=true, never in production). Otherwise it is
			// not registered at all (404), so it cannot map the API for an
			// unauthenticated caller.
			if deps.Config.EnableSwagger {
				r.With(swaggerHeaders).Get("/swagger/*", httpSwagger.Handler(
					httpSwagger.URL("/v1/swagger/doc.json"),
				))
			}

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

// swaggerHeaders runs after vmw.SecurityHeaders and swaps only its CSP for
// swaggerCSP; every other security header stays as SecurityHeaders set it.
func swaggerHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", swaggerCSP)
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware configura CORS usando los orígenes de la configuración.
// Si no hay orígenes configurados, usa localhost como fallback seguro.
func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	if len(allowedOrigins) == 0 {
		allowedOrigins = []string{"http://localhost:3000"}
	}
	c := cors.New(cors.Options{
		AllowedOrigins: allowedOrigins,
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders: []string{"Link"},
		// La autenticación es un Bearer token en Authorization, nunca una
		// cookie: el navegador no necesita enviar credenciales cross-origin,
		// y permitirlo solo ampliaría lo que un origen permitido puede hacer
		// con las cookies que el usuario tuviera para este host.
		AllowCredentials: false,
		MaxAge:           300,
	})
	return c.Handler
}
