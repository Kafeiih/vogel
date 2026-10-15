package main

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/reqctx"
)

// ipRateLimiter is a minimal fixed-window rate limiter keyed on the client IP
// that vmw.RequestContext stored in reqctx. It exists because vogel ships no
// limiter of its own (vmw.RateLimitJSON only formats the 429 for
// github.com/go-chi/httprate, which this module does not depend on) and this
// example must not add a dependency just to show one.
//
// It is a SAMPLE, with the limits that implies:
//   - Per process: every replica keeps its own counters, so N replicas allow
//     N times the configured rate. A real deployment shares the counters
//     (Redis, the gateway, the load balancer) instead.
//   - Fixed window: a client can spend its whole budget at the end of one
//     window and again at the start of the next, up to twice the rate for a
//     moment. A token bucket smooths that out.
//   - The key is whatever IP reqctx holds. vmw.RequestContext takes it from
//     r.RemoteAddr, so behind a reverse proxy every client shares the
//     proxy's address -- and one budget -- unless the IP is resolved from a
//     trusted forwarding header upstream of RequestContext.
//   - Memory grows with the number of distinct IPs seen within one window;
//     expired entries are swept at most once per window.
type ipRateLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu        sync.Mutex
	clients   map[string]*rateWindow
	lastSweep time.Time
}

// rateWindow is one client's request count in the window that began at start.
type rateWindow struct {
	start time.Time
	count int
}

// newIPRateLimiter allows each client IP limit requests per window. Both must
// be positive; LoadConfig validates the values main.go passes in.
func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	return &ipRateLimiter{
		limit:   limit,
		window:  window,
		now:     time.Now,
		clients: make(map[string]*rateWindow),
	}
}

// Middleware answers 429 (vogel's RATE_LIMITED error body, plus a
// Retry-After header in seconds) once the client has used up its budget for
// the current window, and calls next otherwise. Mount it downstream of
// vmw.RequestContext, which is what fills the IP it keys on.
func (l *ipRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Without RequestContext there is no IP: every such request shares
		// the "" key, failing closed into one common budget rather than
		// going unlimited.
		info, _ := reqctx.RequestInfoFromContext(r.Context())
		if retryAfter, ok := l.allow(info.IP); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
			response.Error(w, r, http.StatusTooManyRequests, response.CodeRateLimited, "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allow counts one request for key. When the budget is spent it returns false
// and how long until the client's window ends.
func (l *ipRateLimiter) allow(key string) (time.Duration, bool) {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) >= l.window {
		for k, w := range l.clients {
			if now.Sub(w.start) >= l.window {
				delete(l.clients, k)
			}
		}
		l.lastSweep = now
	}

	w, ok := l.clients[key]
	if !ok || now.Sub(w.start) >= l.window {
		l.clients[key] = &rateWindow{start: now, count: 1}
		return 0, true
	}
	if w.count >= l.limit {
		return w.start.Add(l.window).Sub(now), false
	}
	w.count++
	return 0, true
}

// tracked reports how many clients currently hold a window, for tests.
func (l *ipRateLimiter) tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clients)
}
