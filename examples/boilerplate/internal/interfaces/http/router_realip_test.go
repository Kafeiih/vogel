package http

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
	vmw "github.com/kafeiih/vogel/httpx/middleware"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/handler"
)

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

type denyAll struct{}

func (denyAll) Authenticate(context.Context, string) (*auth.Principal, error) {
	return nil, auth.ErrUnauthenticated
}

func (denyAll) IsAllowed(context.Context, authz.Principal, authz.Resource, string) (bool, error) {
	return false, nil
}

func newTestRouter(t *testing.T, trusted string) http.Handler {
	t.Helper()
	prefixes, err := vmw.ParseTrustedProxies(trusted)
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	metrics, err := vmw.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	return NewRouter(Dependencies{
		Config: RouterConfig{
			Timeout:        5 * time.Second,
			TrustedProxies: prefixes,
		},
		Authenticator: denyAll{},
		AuthzChecker:  denyAll{},
		Metrics:       metrics,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		HealthHandler: handler.NewHealthHandler(okPinger{}, nil),
	})
}

func countRateLimited(router http.Handler, peer string, n int) int {
	limited := 0
	for i := range n {
		req := httptest.NewRequest(http.MethodGet, "/v1/livez", nil)
		req.RemoteAddr = peer
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.%d.%d", i/250, i%250+1))
		req.Header.Set("X-Real-IP", fmt.Sprintf("203.0.%d.%d", i/250, i%250+1))
		req.Header.Set("True-Client-IP", fmt.Sprintf("192.0.%d.%d", i/250, i%250+1))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	return limited
}

func TestNewRouter_SpoofedForwardingHeadersDoNotBypassRateLimit(t *testing.T) {
	router := newTestRouter(t, "")
	if got := countRateLimited(router, "203.0.113.7:4242", 105); got != 5 {
		t.Errorf("rate-limited responses = %d, want 5 (headers from an untrusted peer must be ignored)", got)
	}
}

func TestNewRouter_TrustedProxyForwardsClientIP(t *testing.T) {
	router := newTestRouter(t, "10.0.0.0/8")
	if got := countRateLimited(router, "10.0.0.1:4242", 105); got != 0 {
		t.Errorf("rate-limited responses = %d, want 0 (each forwarded client has its own bucket)", got)
	}
}
