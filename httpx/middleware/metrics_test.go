package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestNewMetrics_DoubleConstruction_DoesNotPanic(t *testing.T) {
	reg := prometheus.NewRegistry()

	if _, err := NewMetrics(reg); err != nil {
		t.Fatalf("first NewMetrics: unexpected error: %v", err)
	}

	m2, err := NewMetrics(reg)
	if err != nil {
		t.Fatalf("second NewMetrics against the same registerer: unexpected error: %v", err)
	}
	if m2 == nil {
		t.Fatal("expected non-nil Metrics from second construction")
	}

	r := chi.NewRouter()
	r.Use(m2.Middleware)
	r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestNewMetrics_NilRegisterer_UsesDefault(t *testing.T) {
	if _, err := NewMetrics(nil); err != nil {
		t.Fatalf("first NewMetrics(nil): unexpected error: %v", err)
	}

	// A second construction against the (shared, package-level) default
	// registerer must not panic or error either.
	if _, err := NewMetrics(nil); err != nil {
		t.Fatalf("second NewMetrics(nil): unexpected error: %v", err)
	}
}

func TestMetrics_Middleware_RecordsRequest(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetrics(reg)
	if err != nil {
		t.Fatalf("NewMetrics: unexpected error: %v", err)
	}

	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	})

	req := httptest.NewRequest(http.MethodGet, "/items/42", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	count := testutil.ToFloat64(m.requestsTotal.WithLabelValues(http.MethodGet, "/items/{id}", "201"))
	if count != 1 {
		t.Errorf("requestsTotal = %v, want 1", count)
	}
}

func newTestMetrics(t *testing.T, opts ...MetricsOption) *Metrics {
	t.Helper()
	m, err := NewMetrics(prometheus.NewRegistry(), opts...)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	return m
}

func TestMetrics_Middleware_Counts5xxWithItsRoutePattern(t *testing.T) {
	m := newTestMetrics(t)
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/items/42", nil))

	if got := testutil.ToFloat64(m.requestsTotal.WithLabelValues(http.MethodGet, "/items/{id}", "503")); got != 1 {
		t.Errorf("requestsTotal{503} = %v, want 1", got)
	}
}

func TestMetrics_Middleware_UnmatchedRouteIsUnknown(t *testing.T) {
	m := newTestMetrics(t)
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/items/{id}", func(http.ResponseWriter, *http.Request) {})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/no/such/path", nil))

	if got := testutil.ToFloat64(m.requestsTotal.WithLabelValues(http.MethodGet, "unknown", "404")); got != 1 {
		t.Errorf(`requestsTotal{path="unknown",status="404"} = %v, want 1`, got)
	}
	if n := testutil.CollectAndCount(m.requestsTotal); n != 1 {
		t.Errorf("requestsTotal has %d series, want 1 (the raw path must not become a label)", n)
	}
}

// Outside a chi router there is no route context; the request is still
// counted, under "unknown", instead of panicking.
func TestMetrics_Middleware_WithoutChiIsUnknown(t *testing.T) {
	m := newTestMetrics(t)
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if got := testutil.ToFloat64(m.requestsTotal.WithLabelValues(http.MethodGet, "unknown", "200")); got != 1 {
		t.Errorf(`requestsTotal{path="unknown",status="200"} = %v, want 1`, got)
	}
}

// With the boilerplate's order (Recovery outside Metrics), a panicking
// handler must still be counted as a 500 under its route pattern, and the
// panic must still reach Recovery, which logs it with the request fields.
func TestMetrics_Middleware_PanicIsCountedAs500AndReachesRecovery(t *testing.T) {
	m := newTestMetrics(t)
	var buf bytes.Buffer
	r := chi.NewRouter()
	r.Use(Recovery(slog.New(slog.NewJSONHandler(&buf, nil))))
	r.Use(m.Middleware)
	r.Get("/items/{id}", func(http.ResponseWriter, *http.Request) {
		panic(errors.New("boom"))
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items/42", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := testutil.ToFloat64(m.requestsTotal.WithLabelValues(http.MethodGet, "/items/{id}", "500")); got != 1 {
		t.Errorf("requestsTotal{500} = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(m.requestDuration); got != 1 {
		t.Errorf("requestDuration has %d series, want 1", got)
	}

	var line map[string]any
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if err := json.Unmarshal(lines[len(lines)-1], &line); err != nil {
		t.Fatalf("decode recovery log %q: %v", buf.String(), err)
	}
	if line["msg"] != "panic recovered" || line["path"] != "/items/42" || line["method"] != http.MethodGet || line["error"] != "boom" {
		t.Errorf("recovery log = %v, want msg, path, method and error of the request", line)
	}
}

func TestNewMetrics_WithDurationBuckets(t *testing.T) {
	m := newTestMetrics(t, WithDurationBuckets(0.1, 1, 10))
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/x", func(http.ResponseWriter, *http.Request) {})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	h, err := m.requestDuration.GetMetricWithLabelValues(http.MethodGet, "/x")
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	pb := &dto.Metric{}
	if err := h.(prometheus.Histogram).Write(pb); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buckets := pb.GetHistogram().GetBucket()
	got := make([]float64, 0, len(buckets))
	for _, b := range buckets {
		got = append(got, b.GetUpperBound())
	}
	if len(got) != 3 || got[0] != 0.1 || got[1] != 1 || got[2] != 10 {
		t.Errorf("buckets = %v, want [0.1 1 10]", got)
	}
}

func TestNewMetrics_DefaultDurationBucketsUnchanged(t *testing.T) {
	want := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	got := DefaultDurationBuckets()
	if len(got) != len(want) {
		t.Fatalf("DefaultDurationBuckets() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DefaultDurationBuckets() = %v, want %v", got, want)
		}
	}
}

func TestNewMetrics_RejectsInvalidDurationBuckets(t *testing.T) {
	for name, buckets := range map[string][]float64{
		"empty":          {},
		"not increasing": {1, 0.5},
		"duplicate":      {1, 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewMetrics(prometheus.NewRegistry(), WithDurationBuckets(buckets...)); err == nil {
				t.Errorf("NewMetrics(WithDurationBuckets(%v)) = nil error, want an error", buckets)
			}
		})
	}
}
