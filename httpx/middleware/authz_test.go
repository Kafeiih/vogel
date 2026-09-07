package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
)

// fakeChecker implements authz.Checker with a canned result and records the
// resource it was asked to check, so RequirePermission's status mapping and
// wildcard-ID behavior can be tested without a real PDP.
type fakeChecker struct {
	allowed     bool
	err         error
	gotResource authz.Resource
}

func (f *fakeChecker) IsAllowed(_ context.Context, _ authz.Principal, res authz.Resource, _ string) (bool, error) {
	f.gotResource = res
	return f.allowed, f.err
}

func withPrincipal(r *http.Request, p *auth.Principal) *http.Request {
	return r.WithContext(auth.WithPrincipal(r.Context(), p))
}

func TestRequirePermission_NoPrincipal_Returns401AndDoesNotCallNext(t *testing.T) {
	fc := &fakeChecker{allowed: true}
	called := false
	handler := RequirePermission(fc, "invoices", "read", slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("next.ServeHTTP was called without an authenticated principal (fail-open)")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestRequirePermission_Allowed_CallsNext(t *testing.T) {
	fc := &fakeChecker{allowed: true}
	called := false
	handler := RequirePermission(fc, "invoices", "read", slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), &auth.Principal{UserID: "u1"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("next.ServeHTTP was not called despite the checker allowing the request")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestRequirePermission_Denied_Returns403AndDoesNotCallNext(t *testing.T) {
	fc := &fakeChecker{allowed: false}
	called := false
	handler := RequirePermission(fc, "invoices", "read", slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), &auth.Principal{UserID: "u1"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("next.ServeHTTP was called despite a genuine denial (fail-open)")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestRequirePermission_CheckerError_Returns503NotForbidden(t *testing.T) {
	fc := &fakeChecker{err: errors.New("cerbos: connection refused")}
	called := false
	handler := RequirePermission(fc, "invoices", "read", slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), &auth.Principal{UserID: "u1"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("next.ServeHTTP was called despite a PDP error (fail-open)")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d (a PDP outage must not surface as 403 or 500 — see FIX 2, supersedes go-licencias DEC-08)", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestRequirePermission_NoURLParam_UsesWildcardResourceID(t *testing.T) {
	fc := &fakeChecker{allowed: true}
	handler := RequirePermission(fc, "invoices", "list", slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := withPrincipal(httptest.NewRequest(http.MethodGet, "/invoices", nil), &auth.Principal{UserID: "u1"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if fc.gotResource.ID != wildcardResourceID {
		t.Errorf("resource ID = %q, want wildcard %q (Cerbos rejects an empty resource ID)", fc.gotResource.ID, wildcardResourceID)
	}
}

func TestRequirePermission_URLParamID_IsUsedAsResourceID(t *testing.T) {
	fc := &fakeChecker{allowed: true}
	r := chi.NewRouter()
	r.Get("/invoices/{id}", RequirePermission(fc, "invoices", "read", slog.Default())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
	).ServeHTTP)

	req := withPrincipal(httptest.NewRequest(http.MethodGet, "/invoices/abc123", nil), &auth.Principal{UserID: "u1"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if fc.gotResource.ID != "abc123" {
		t.Errorf("resource ID = %q, want %q", fc.gotResource.ID, "abc123")
	}
}
