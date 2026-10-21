package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kafeiih/vogel/access"
	"github.com/kafeiih/vogel/authz"
	vmw "github.com/kafeiih/vogel/httpx/middleware"
)

// fakeClock is a manually advanced clock for ipRateLimiter.now.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

// newTestLimiter returns a limiter on a fake clock and the handler chain it
// guards. vmw.RequestContext runs first, exactly as in NewRouter, so the
// limiter keys on the client IP vogel resolved rather than on anything the
// test injects into the context by hand.
func newTestLimiter(limit int, window time.Duration) (*ipRateLimiter, *fakeClock, http.Handler) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	l := newIPRateLimiter(limit, window)
	l.now = clock.now
	return l, clock, vmw.RequestContext(l.Middleware(http.HandlerFunc(okHandler)))
}

func requestFrom(t *testing.T, h http.Handler, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/documents", nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIPRateLimiter_RejectsRequestOverTheLimit(t *testing.T) {
	const limit = 3
	_, _, h := newTestLimiter(limit, time.Minute)

	for i := range limit {
		if rec := requestFrom(t, h, "203.0.113.7:1234"); rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200 (body %s)", i+1, rec.Code, rec.Body.String())
		}
	}

	rec := requestFrom(t, h, "203.0.113.7:5678") // another port, same client IP
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("request %d = %d, want 429", limit+1, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "RATE_LIMITED") {
		t.Errorf("body = %s, want vogel's RATE_LIMITED error code", rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want %q", got, "60")
	}
}

func TestIPRateLimiter_ClientsAreIndependent(t *testing.T) {
	_, _, h := newTestLimiter(1, time.Minute)

	if rec := requestFrom(t, h, "203.0.113.7:1234"); rec.Code != http.StatusOK {
		t.Fatalf("first client, first request = %d, want 200", rec.Code)
	}
	if rec := requestFrom(t, h, "203.0.113.7:1234"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("first client, second request = %d, want 429", rec.Code)
	}
	if rec := requestFrom(t, h, "198.51.100.9:1234"); rec.Code != http.StatusOK {
		t.Errorf("second client = %d, want 200: one client's budget must not throttle another", rec.Code)
	}
}

func TestIPRateLimiter_WindowResets(t *testing.T) {
	l, clock, h := newTestLimiter(1, time.Minute)

	requestFrom(t, h, "203.0.113.7:1234")
	clock.t = clock.t.Add(30 * time.Second)
	rec := requestFrom(t, h, "203.0.113.7:1234")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("inside the window = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want %q (seconds left in the window)", got, "30")
	}

	clock.t = clock.t.Add(30 * time.Second)
	if rec := requestFrom(t, h, "203.0.113.7:1234"); rec.Code != http.StatusOK {
		t.Errorf("after the window = %d, want 200", rec.Code)
	}
	if n := l.tracked(); n != 1 {
		t.Errorf("tracked clients = %d, want 1", n)
	}
}

// Entries for clients that went quiet are dropped once their window has
// passed, so the map does not grow with every IP ever seen.
func TestIPRateLimiter_SweepsExpiredClients(t *testing.T) {
	l, clock, h := newTestLimiter(5, time.Minute)

	for _, ip := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"} {
		requestFrom(t, h, ip+":1234")
	}
	if n := l.tracked(); n != 3 {
		t.Fatalf("tracked clients = %d, want 3", n)
	}

	clock.t = clock.t.Add(time.Minute)
	requestFrom(t, h, "198.51.100.9:1234")
	if n := l.tracked(); n != 1 {
		t.Errorf("tracked clients after a window = %d, want 1 (only the new client)", n)
	}
}

// NewRouter checks the budget before Authenticate, so unauthenticated
// requests (a client guessing tokens) are throttled too, and leaves /healthz
// outside it.
func TestNewRouter_RateLimitsAPIBeforeAuthentication(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics, err := vmw.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	var checker authz.Checker = denyAll{}
	h := NewRouter(nil, nil, fakeAuthenticator{}, checker, access.New(checker), metrics, newIPRateLimiter(1, time.Minute), newFakeStorage("http://localhost").Handler(), nil, logger)

	if rec := requestFrom(t, h, "203.0.113.7:1234"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("first unauthenticated request = %d, want 401", rec.Code)
	}
	if rec := requestFrom(t, h, "203.0.113.7:1234"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second unauthenticated request = %d, want 429", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "203.0.113.7:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusTooManyRequests {
		t.Error("/healthz answered 429; it must stay outside the API rate limit")
	}
}
