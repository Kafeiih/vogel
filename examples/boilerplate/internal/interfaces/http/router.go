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

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
	vmw "github.com/kafeiih/vogel/httpx/middleware"
	"github.com/kafeiih/vogel/httpx/openapi"

	"github.com/kafeiih/vogel/examples/boilerplate/api"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/apigen"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/handler"
)

func clientIPKey(r *http.Request) (string, error) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	return httprate.CanonicalizeIP(ip), nil
}

type RouterConfig struct {
	AppURL      string
	Timeout     time.Duration
	CORSOrigins []string

	TrustedProxies []netip.Prefix

	// EnableSwagger serves the API contract (api/openapi.yaml) at
	// /openapi.yaml. main.go sets it from SWAGGER_ENABLED.
	EnableSwagger bool
}

var authMessages = vmw.WithAuthMessages(vmw.AuthMessages{
	Unauthorized:       "Autenticación requerida",
	Forbidden:          "No tenés permisos para realizar esta acción",
	ServiceUnavailable: "Servicio no disponible temporalmente",
})

type Dependencies struct {
	Config        RouterConfig
	Authenticator auth.Authenticator
	AuthzChecker  authz.Checker
	Metrics       *vmw.Metrics
	Logger        *slog.Logger
	HealthHandler *handler.HealthHandler
	AuditHandler  *handler.AuditHandler
	// vogel:component bpms begin

	ProcessHandler *handler.ProcessHandler
	// vogel:component bpms end
	// vogel:component tracing begin

	Tracing func(http.Handler) http.Handler
	// vogel:component tracing end
}

func NewRouter(deps Dependencies) *chi.Mux {
	r := chi.NewRouter()

	// vogel:component tracing begin
	if deps.Tracing != nil {
		r.Use(deps.Tracing)
	}

	// vogel:component tracing end
	r.Use(vmw.SecurityHeaders)

	r.Use(vmw.RequestID)

	r.Use(vmw.RealIP(deps.Config.TrustedProxies...))

	r.Use(vmw.RequestContext)

	r.Use(vmw.StructuredLogger(deps.Logger))
	r.Use(vmw.Recovery(deps.Logger))
	r.Use(deps.Metrics.Middleware)
	r.Use(corsMiddleware(deps.Config.CORSOrigins))

	r.Use(httprate.LimitBy(100, 1*time.Minute, clientIPKey,
		httprate.WithLimitHandler(vmw.RateLimitJSON()),
	))

	r.Use(middleware.Timeout(deps.Config.Timeout))

	ops := apigen.NewStrictHandlerWithOptions(handler.API{AuditHandler: deps.AuditHandler}, nil, apigen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  openapi.RequestErrorHandler,
		ResponseErrorHandlerFunc: openapi.ResponseErrorHandler(deps.Logger),
	})

	r.Group(func(r chi.Router) {
		if deps.Config.EnableSwagger {
			r.Get("/openapi.yaml", openapi.ServeSpec(api.Spec()).ServeHTTP)
		}

		r.Get("/healthz", deps.HealthHandler.Check)
		r.Get("/readyz", deps.HealthHandler.Ready)
		r.Get("/livez", deps.HealthHandler.Live)

		r.Group(func(r chi.Router) {
			r.Use(vmw.Authenticate(deps.Authenticator, deps.Logger, authMessages))

			r.Use(httprate.LimitBy(1000, 1*time.Minute,
				func(r *http.Request) (string, error) {
					if principal := auth.FromContext(r.Context()); principal != nil {
						return principal.UserID, nil
					}
					return clientIPKey(r)
				},
				httprate.WithLimitHandler(vmw.RateLimitJSON()),
			))

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

				// The generated params are left empty: handlers read and validate the request with vogel.
				r.With(vmw.RequirePermission(deps.AuthzChecker, "audit:entry", "list", deps.Logger, authMessages), openapi.RequestInContext).
					Get("/", func(w http.ResponseWriter, r *http.Request) {
						ops.ListAuditEntries(w, r, apigen.ListAuditEntriesParams{})
					})
				r.With(vmw.RequirePermission(deps.AuthzChecker, "audit:entry", "view", deps.Logger, authMessages), openapi.RequestInContext).
					Get("/{id}", func(w http.ResponseWriter, r *http.Request) {
						ops.GetAuditEntry(w, r, chi.URLParam(r, "id"))
					})
			})
			// vogel:component bpms begin

			if deps.ProcessHandler != nil {
				mountProcessRoutes(r, deps)
			}
			// vogel:component bpms end
		})
	})

	return r
}

// vogel:component bpms begin
func mountProcessRoutes(r chi.Router, deps Dependencies) {
	deps.ProcessHandler.Mount(r, func(kind, action string) func(http.Handler) http.Handler {
		return vmw.RequirePermission(deps.AuthzChecker, kind, action, deps.Logger, authMessages)
	})
}

// vogel:component bpms end
func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	if len(allowedOrigins) == 0 {
		allowedOrigins = []string{"http://localhost:3000"}
	}
	c := cors.New(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	})
	return c.Handler
}
