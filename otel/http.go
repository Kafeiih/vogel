package otel

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// MiddlewareOption configures Middleware.
type MiddlewareOption func(*middlewareConfig)

type middlewareConfig struct {
	trusted bool
	tp      trace.TracerProvider // nil: the global one
}

// WithTrustedPropagation makes Middleware continue the trace of an incoming
// traceparent, and accept incoming baggage. Use it only when every request
// reaches the service through a gateway or service mesh that sets or
// sanitizes those headers.
func WithTrustedPropagation() MiddlewareOption {
	return func(c *middlewareConfig) { c.trusted = true }
}

// WithTracerProvider sets the TracerProvider the middleware uses instead of
// the global one Setup installs.
func WithTracerProvider(tp trace.TracerProvider) MiddlewareOption {
	return func(c *middlewareConfig) { c.tp = tp }
}

// Middleware returns an HTTP server middleware that records a span per
// request with otelhttp. Mount it first, so the request ID, logger, recovery
// and metrics middlewares all run inside the span.
//
// An incoming traceparent is client input, like X-Request-Id, so by default
// it is not adopted: every request starts a new trace, sampled by the
// service's own ratio, with a span link to the client's span when it sent
// one; incoming baggage is ignored. WithTrustedPropagation continues the
// client's trace instead.
//
// The span is named "METHOD /route/{pattern}" after the chi route pattern
// once the handler has run, and carries http.route; a request no route
// matched keeps the method alone, so raw paths never become span names.
func Middleware(opts ...MiddlewareOption) func(http.Handler) http.Handler {
	cfg := middlewareConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	// spanName is the only source of the span's name: otelhttp calls it when
	// the span starts and again after the handler when r.Pattern is set
	// (chi sets it), so it must give the same answer as nameAfterRoute.
	hopts := []otelhttp.Option{
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return spanName(r) }),
	}
	if cfg.tp != nil {
		hopts = append(hopts, otelhttp.WithTracerProvider(cfg.tp))
	}
	if cfg.trusted {
		hopts = append(hopts, otelhttp.WithPropagators(
			propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})))
	} else {
		hopts = append(hopts,
			otelhttp.WithPublicEndpointFn(func(*http.Request) bool { return true }),
			otelhttp.WithPropagators(propagation.TraceContext{}),
		)
	}
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(nameAfterRoute(next), "http.server", hopts...)
	}
}

// nameAfterRoute runs next, then names the request's span after the chi
// route pattern and sets http.route.
func nameAfterRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		pattern := routePattern(r)
		if pattern == "" {
			return
		}
		span := trace.SpanFromContext(r.Context())
		span.SetName(spanName(r))
		span.SetAttributes(attribute.String("http.route", pattern))
	})
}

// spanName is "METHOD /route/{pattern}" once chi has matched a route, and
// the method alone before that or when no route matched.
func spanName(r *http.Request) string {
	if pattern := routePattern(r); pattern != "" {
		return r.Method + " " + pattern
	}
	return r.Method
}

// routePattern returns the chi route pattern matched for r, or "".
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		return rctx.RoutePattern()
	}
	return ""
}
