package http

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	vmw "github.com/kafeiih/vogel/httpx/middleware"

	"github.com/kafeiih/vogel/examples/boilerplate/api"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/handler"
)

func newRouterWithConfig(t *testing.T, cfg RouterConfig) http.Handler {
	t.Helper()
	metrics, err := vmw.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(Dependencies{
		Config:        cfg,
		Authenticator: denyAll{},
		AuthzChecker:  denyAll{},
		Metrics:       metrics,
		Logger:        logger,
		HealthHandler: handler.NewHealthHandler(okPinger{}, logger),
	})
}

func get(router http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "203.0.113.9:1234"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestNewRouter_SpecAbsentWhenDisabled(t *testing.T) {
	router := newRouterWithConfig(t, RouterConfig{})
	for _, path := range []string{"/openapi.yaml", "/swagger/index.html"} {
		if rec := get(router, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

func TestNewRouter_SpecServedWithoutAuthenticationWhenEnabled(t *testing.T) {
	router := newRouterWithConfig(t, RouterConfig{EnableSwagger: true})
	rec := get(router, "/openapi.yaml")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /openapi.yaml = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/yaml" {
		t.Errorf("Content-Type = %q, want application/yaml", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), api.Spec()) {
		t.Error("body is not api/openapi.yaml")
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY (SecurityHeaders must still apply)", got)
	}
}

func TestNewRouter_CORSDoesNotAllowCredentials(t *testing.T) {
	router := newRouterWithConfig(t, RouterConfig{CORSOrigins: []string{"https://app.example.org"}})
	req := httptest.NewRequest(http.MethodOptions, "/audit/", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("Origin", "https://app.example.org")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	req.Header.Set("Access-Control-Request-Headers", "authorization")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.org" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want the configured origin (preflight not handled?)", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want it absent", got)
	}
}

func assertSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for header, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'none'",
		"Cache-Control":           "private, no-store",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q (status %d)", header, got, want, rec.Code)
		}
	}
}

func TestNewRouter_EarlyRejectionsCarrySecurityHeaders(t *testing.T) {
	t.Run("401 from Authenticate", func(t *testing.T) {
		router := newRouterWithConfig(t, RouterConfig{})
		rec := get(router, "/audit/")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET /audit/ without a token = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		assertSecurityHeaders(t, rec)
	})

	t.Run("404 for an unknown route", func(t *testing.T) {
		router := newRouterWithConfig(t, RouterConfig{})
		rec := get(router, "/nope")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET /nope = %d, want %d", rec.Code, http.StatusNotFound)
		}
		assertSecurityHeaders(t, rec)
	})

	t.Run("429 from the global per-IP limiter", func(t *testing.T) {
		router := newRouterWithConfig(t, RouterConfig{})
		var rec *httptest.ResponseRecorder
		for range 101 {
			rec = get(router, "/livez")
		}
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("101st GET /livez = %d, want %d", rec.Code, http.StatusTooManyRequests)
		}
		assertSecurityHeaders(t, rec)
	})
}
