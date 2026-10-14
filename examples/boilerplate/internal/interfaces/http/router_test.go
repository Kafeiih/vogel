package http

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	vmw "github.com/kafeiih/vogel/httpx/middleware"

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

// In production (EnableSwagger false, the zero value) the API description is
// not served at all: it maps every route and parameter for an attacker.
func TestNewRouter_SwaggerAbsentWhenDisabled(t *testing.T) {
	router := newRouterWithConfig(t, RouterConfig{})
	for _, path := range []string{"/v1/swagger/index.html", "/v1/swagger/doc.json"} {
		if rec := get(router, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

// Outside production the UI is served under SecurityHeaders, with a CSP
// relaxed only as far as swagger-ui needs (its inline bootstrap script and
// styles) and framing still denied.
func TestNewRouter_SwaggerServedWithItsOwnCSPWhenEnabled(t *testing.T) {
	router := newRouterWithConfig(t, RouterConfig{EnableSwagger: true})
	rec := get(router, "/v1/swagger/index.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/swagger/index.html = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != swaggerCSP {
		t.Errorf("Content-Security-Policy = %q, want %q", got, swaggerCSP)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY (SecurityHeaders must still apply)", got)
	}
}

// Authentication is a Bearer token in the Authorization header, never a
// cookie, so CORS must not tell browsers to send credentials cross-origin.
func TestNewRouter_CORSDoesNotAllowCredentials(t *testing.T) {
	router := newRouterWithConfig(t, RouterConfig{CORSOrigins: []string{"https://app.example.org"}})
	req := httptest.NewRequest(http.MethodOptions, "/v1/audit/", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("Origin", "https://app.example.org")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	// Browsers send the requested header names lowercased (Fetch spec).
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
