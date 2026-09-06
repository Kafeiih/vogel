package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
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
