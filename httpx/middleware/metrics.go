package middleware

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics is an HTTP middleware that records Prometheus metrics (request
// count, latency, and response size) for every request.
//
// Unlike a package-level prometheus.MustRegister in an init() function,
// Metrics is safe to construct more than once against the same Registerer:
// a prometheus.AlreadyRegisteredError is handled by reusing the already
// registered collector instead of panicking. This matters for a library —
// a consumer may build two server instances, or a test may construct a
// Metrics per test case.
type Metrics struct {
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	responseSize    *prometheus.HistogramVec
}

// NewMetrics creates a Metrics middleware, registering its collectors on reg.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	requestsTotal, err := registerCounterVec(reg, prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests.",
		},
		[]string{"method", "path", "status"},
	))
	if err != nil {
		return nil, err
	}

	requestDuration, err := registerHistogramVec(reg, prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"method", "path"},
	))
	if err != nil {
		return nil, err
	}

	responseSize, err := registerHistogramVec(reg, prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_response_size_bytes",
			Help:    "HTTP response size in bytes.",
			Buckets: []float64{100, 500, 1000, 5000, 10000, 50000, 100000},
		},
		[]string{"method", "path"},
	))
	if err != nil {
		return nil, err
	}

	return &Metrics{
		requestsTotal:   requestsTotal,
		requestDuration: requestDuration,
		responseSize:    responseSize,
	}, nil
}

// registerCounterVec registers cv on reg, returning the already-registered
// collector instead of erroring when cv is a duplicate registration.
func registerCounterVec(reg prometheus.Registerer, cv *prometheus.CounterVec) (*prometheus.CounterVec, error) {
	if err := reg.Register(cv); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(*prometheus.CounterVec); ok {
				return existing, nil
			}
		}
		return nil, err
	}
	return cv, nil
}

// registerHistogramVec registers hv on reg, returning the already-registered
// collector instead of erroring when hv is a duplicate registration.
func registerHistogramVec(reg prometheus.Registerer, hv *prometheus.HistogramVec) (*prometheus.HistogramVec, error) {
	if err := reg.Register(hv); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(*prometheus.HistogramVec); ok {
				return existing, nil
			}
		}
		return nil, err
	}
	return hv, nil
}

// Middleware wraps next, recording request count, latency, and response size
// labeled by method and chi route pattern.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		// Use the chi route pattern (e.g. "/v1/audit/{id}") instead of the
		// actual path to avoid high-cardinality labels.
		routePattern := chi.RouteContext(r.Context()).RoutePattern()
		if routePattern == "" {
			routePattern = "unknown"
		}

		status := strconv.Itoa(ww.Status())
		elapsed := time.Since(start).Seconds()

		m.requestsTotal.WithLabelValues(r.Method, routePattern, status).Inc()
		m.requestDuration.WithLabelValues(r.Method, routePattern).Observe(elapsed)
		m.responseSize.WithLabelValues(r.Method, routePattern).Observe(float64(ww.BytesWritten()))
	})
}
