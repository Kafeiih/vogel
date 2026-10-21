package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/kafeiih/vogel/access"
	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
	vmw "github.com/kafeiih/vogel/httpx/middleware"
)

// publicRoutes are the only routes NewRouter may serve without an access
// guard. Adding a public route means adding it here, on purpose.
var publicRoutes = map[string]bool{
	"GET /healthz": true,
	// The fake bucket: authorized by the presigned URL's signature, never
	// by a bearer token (see fakeStorage.Handler).
	"GET /fake-storage/*": true,
	"PUT /fake-storage/*": true,
}

// acceptAll authenticates every bearer token as the same plain user.
type acceptAll struct{}

func (acceptAll) Authenticate(context.Context, string) (*auth.Principal, error) {
	return &auth.Principal{UserID: "probe", Username: "probe"}, nil
}

// denyAll is an authz.Checker that denies every action.
type denyAll struct{}

func (denyAll) IsAllowed(context.Context, authz.Principal, authz.Resource, string) (bool, error) {
	return false, nil
}

var routeParam = regexp.MustCompile(`\{[^}]*\}`)

// TestNewRouter_EveryAPIRouteIsGuarded fails if a route under /api/ can be
// reached without passing RequirePermission or RequireAccess.
//
// How: the router is built with an authenticator that accepts any token and
// an authz checker (and the access.Guard built on it) that denies
// everything. chi.Walk lists every registered route, and each one is then
// requested with a valid bearer token. A guarded route answers 403 from its
// guard before the handler runs; a route without a guard reaches its
// handler, whose dependencies are nil here, so it answers anything else
// (typically a 500 from vmw.Recovery). This checks behaviour, not how the
// route was written: it does not depend on middleware function names, on
// r.With versus r.Use, or on how chi nests sub-routers, so it keeps working
// when routes are reorganised. Every route not listed in publicRoutes is
// probed, so a new unauthenticated endpoint is a deliberate choice.
func TestNewRouter_EveryAPIRouteIsGuarded(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics, err := vmw.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	var checker authz.Checker = denyAll{}
	handler := NewRouter(nil, nil, acceptAll{}, checker, access.New(checker), metrics, newIPRateLimiter(1_000, time.Minute), newFakeStorage("http://localhost").Handler(), nil, logger)

	mux, ok := handler.(chi.Routes)
	if !ok {
		t.Fatalf("NewRouter returned %T, which chi.Walk cannot inspect", handler)
	}

	var apiRoutes int
	walk := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		if publicRoutes[key] {
			return nil
		}
		apiRoutes++

		path := routeParam.ReplaceAllString(route, "probe")
		path = strings.ReplaceAll(path, "*", "probe")
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer probe")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s answered %d with every action denied, want 403: the route has no access guard", key, rec.Code)
		}
		return nil
	}
	if err := chi.Walk(mux, walk); err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	if apiRoutes == 0 {
		t.Fatal("chi.Walk found no guarded routes; the test is not inspecting the real router")
	}
}

// The probe itself must catch an unguarded route, or a passing
// TestNewRouter_EveryAPIRouteIsGuarded proves nothing.
func TestGuardProbe_DetectsUnguardedRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := chi.NewRouter()
	r.Use(vmw.Recovery(logger))
	r.Group(func(r chi.Router) {
		r.Use(vmw.Authenticate(acceptAll{}, logger))
		r.With(vmw.RequirePermission(denyAll{}, "x", "read", logger)).Get("/guarded", okHandler)
		r.Get("/unguarded", okHandler)
	})

	codes := map[string]int{}
	for _, path := range []string{"/guarded", "/unguarded"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer probe")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		codes[path] = rec.Code
	}
	if codes["/guarded"] != http.StatusForbidden {
		t.Errorf("guarded route = %d, want 403", codes["/guarded"])
	}
	if codes["/unguarded"] == http.StatusForbidden {
		t.Error("unguarded route = 403; the probe could not tell it from a guarded one")
	}
}

func okHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// /metrics lives only on the internal listener, and that listener serves
// nothing else.
func TestNewMetricsServer_ServesOnlyMetrics(t *testing.T) {
	srv := newMetricsServer("127.0.0.1:0", prometheus.NewRegistry())
	for path, want := range map[string]int{"/metrics": http.StatusOK, "/healthz": http.StatusNotFound} {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("GET %s on the metrics listener = %d, want %d", path, rec.Code, want)
		}
	}
}
