package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

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
	exemplars       ExemplarFunc // nil: no exemplars
}

// ExemplarFunc returns the trace and span IDs of the span carried by ctx, or
// empty strings when there is none (or it was not sampled). It has the shape
// of logger.TraceIDsFunc; otel.SampledTraceIDs implements it.
type ExemplarFunc func(ctx context.Context) (traceID, spanID string)

// DefaultDurationBuckets returns the http_request_duration_seconds buckets
// NewMetrics uses when WithDurationBuckets is not given: 5 ms to 10 s.
func DefaultDurationBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
}

// MetricsOption configures NewMetrics.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	durationBuckets []float64
	exemplars       ExemplarFunc
}

// WithExemplars attaches the request's trace_id and span_id, read with fn,
// as an exemplar to each http_request_duration_seconds observation, so a
// slow bucket links to a trace. A request fn finds no trace for records no
// exemplar. Prometheus reads exemplars only in the OpenMetrics format:
// serve the registry with promhttp.HandlerOpts{EnableOpenMetrics: true}.
func WithExemplars(fn ExemplarFunc) MetricsOption {
	return func(c *metricsConfig) { c.exemplars = fn }
}

// WithDurationBuckets sets the http_request_duration_seconds bucket upper
// bounds, in seconds. They must be non-empty and strictly increasing, or
// NewMetrics fails. Use it when the defaults do not bracket the service's
// latency objective, so the SLO threshold falls on a bucket boundary.
//
// Buckets are fixed when the collector is first registered: a later NewMetrics
// on the same Registerer reuses that collector and its buckets.
func WithDurationBuckets(buckets ...float64) MetricsOption {
	return func(c *metricsConfig) { c.durationBuckets = append([]float64(nil), buckets...) }
}

// NewMetrics creates a Metrics middleware, registering its collectors on reg.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewMetrics(reg prometheus.Registerer, opts ...MetricsOption) (*Metrics, error) {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	cfg := metricsConfig{durationBuckets: DefaultDurationBuckets()}
	for _, opt := range opts {
		opt(&cfg)
	}
	if err := validateBuckets(cfg.durationBuckets); err != nil {
		return nil, err
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
			Buckets: cfg.durationBuckets,
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
		exemplars:       cfg.exemplars,
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
//
// The request is recorded in a deferred call, so a handler that panics is
// still counted, as a 500, before the panic continues to an outer Recovery
// (the order the examples use). Outside a chi router, or for a request no
// route matched, the path label is "unknown".
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

		defer func() {
			rec := recover()

			code := ww.Status()
			if rec != nil {
				code = http.StatusInternalServerError
			}
			routePattern := routePatternOf(r)
			m.requestsTotal.WithLabelValues(r.Method, routePattern, strconv.Itoa(code)).Inc()
			observe(r.Context(), m.requestDuration.WithLabelValues(r.Method, routePattern), time.Since(start).Seconds(), m.exemplars)
			m.responseSize.WithLabelValues(r.Method, routePattern).Observe(float64(ww.BytesWritten()))

			if rec != nil {
				panic(rec)
			}
		}()

		next.ServeHTTP(ww, r)
	})
}

// routePatternOf returns the chi route pattern (e.g. "/audit/{id}") used
// as the path label instead of the actual path, to avoid high-cardinality
// labels, or "unknown" when there is none.
func routePatternOf(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return "unknown"
	}
	if p := rctx.RoutePattern(); p != "" {
		return p
	}
	return "unknown"
}

// validateBuckets rejects bucket bounds prometheus would panic on.
func validateBuckets(buckets []float64) error {
	if len(buckets) == 0 {
		return errors.New("httpx/middleware: duration buckets must not be empty")
	}
	for i := 1; i < len(buckets); i++ {
		if buckets[i] <= buckets[i-1] {
			return fmt.Errorf("httpx/middleware: duration buckets must be strictly increasing, got %v", buckets)
		}
	}
	return nil
}

// observe records v on o, with the trace of ctx as an exemplar when fn finds
// one.
func observe(ctx context.Context, o prometheus.Observer, v float64, fn ExemplarFunc) {
	if fn != nil {
		if traceID, spanID := fn(ctx); traceID != "" && validExemplar(traceID, spanID) {
			if eo, ok := o.(prometheus.ExemplarObserver); ok {
				eo.ObserveWithExemplar(v, prometheus.Labels{"trace_id": traceID, "span_id": spanID})
				return
			}
		}
	}
	o.Observe(v)
}

// validExemplar reports whether client_golang accepts traceID and spanID as
// exemplar labels: ObserveWithExemplar panics on invalid UTF-8 or past
// prometheus.ExemplarMaxRunes runes of names and values.
func validExemplar(traceID, spanID string) bool {
	return utf8.ValidString(traceID) && utf8.ValidString(spanID) &&
		utf8.RuneCountInString("trace_id"+traceID+"span_id"+spanID) <= prometheus.ExemplarMaxRunes
}
