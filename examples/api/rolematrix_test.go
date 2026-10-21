package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/kafeiih/vogel/access"
	"github.com/kafeiih/vogel/auth"
	vmw "github.com/kafeiih/vogel/httpx/middleware"
)

// outcome is what the router's guards decide for one request: rejected by
// Authenticate (401), rejected by RequirePermission/RequireAccess (403), or
// let through to the handler.
type outcome int

const (
	unauthenticated outcome = iota
	forbidden
	allowed
)

func (o outcome) String() string {
	return [...]string{"401", "403", "allowed"}[o]
}

// ownedDocID is the one document the "owner" role owns; otherDocID belongs
// to nobody in this test.
const (
	ownedDocID = "0b0c8a3e-6a3b-4b8e-9d2a-2f6f1f6f1f6f"
	otherDocID = "7e57d0c5-0000-4000-8000-000000000001"
)

// matrixRoles are fakeAuthenticator tokens ("<userID>:<username>:<roles>");
// an empty token sends no Authorization header at all.
var matrixRoles = []struct {
	name  string
	token string
}{
	{"anonymous", ""},
	{"malformed token", "not-a-token"},
	{"plain user", "u1:alice:"},
	{"author/reviewer", "u2:bob:author,reviewer"},
	{"owner", "owner:olga:"},
	{"admin", "a1:root:admin"},
}

// TestNewRouter_RoleMatrix pins, for every route and every kind of caller,
// whether the router's guards let the request through, under main.go's
// wiring of fakeChecker and access.Guard. A change to a route's guard, to
// fakeChecker's policy or to the guard's ownership resolver shows up here
// as a named route x role cell, not as a silent widening of access.
//
// The handlers are nil (as in TestNewRouter_EveryAPIRouteIsGuarded), so a
// request that passes its guard panics in the handler and vmw.Recovery
// answers 500: anything other than 401 or 403 counts as allowed. The
// handlers' own checks (DocumentHandler.Delete's per-instance "report" rule)
// need a database and are covered by handler_integration_test.go.
func TestNewRouter_RoleMatrix(t *testing.T) {
	A, F, U := allowed, forbidden, unauthenticated
	docPath := "/documents/" + ownedDocID

	// Columns follow matrixRoles: anonymous, malformed, plain, author, owner, admin.
	tests := []struct {
		method, route, path string
		want                [6]outcome
	}{
		{"GET", "/healthz", "/healthz", [6]outcome{A, A, A, A, A, A}},
		// The fake bucket ignores bearer tokens entirely: without a valid
		// presigned signature every caller, admin included, gets its 403.
		{"GET", "/fake-storage/*", "/fake-storage/documents/" + ownedDocID, [6]outcome{F, F, F, F, F, F}},
		{"PUT", "/fake-storage/*", "/fake-storage/documents/" + ownedDocID, [6]outcome{F, F, F, F, F, F}},
		{"POST", "/documents", "/documents", [6]outcome{U, U, F, F, F, A}},
		{"GET", "/documents", "/documents", [6]outcome{U, U, A, A, A, A}},
		{"GET", "/documents/{id}", docPath, [6]outcome{U, U, A, A, A, A}},
		{"GET", "/documents/{id}/history", docPath + "/history", [6]outcome{U, U, A, A, A, A}},
		{"GET", "/documents/{id}/download-url", docPath + "/download-url", [6]outcome{U, U, A, A, A, A}},
		{"POST", "/documents/{id}/upload-url", docPath + "/upload-url", [6]outcome{U, U, F, F, F, A}},
		{"POST", "/documents/{id}/file", docPath + "/file", [6]outcome{U, U, F, F, F, A}},
		{"POST", "/documents/{id}/transitions", docPath + "/transitions", [6]outcome{U, U, F, F, F, A}},
		{"POST", "/documents/{id}/claim", docPath + "/claim", [6]outcome{U, U, F, F, F, A}},
		{"DELETE", "/documents/{id}", docPath, [6]outcome{U, U, F, F, A, A}},
		{"DELETE", "/documents/{id}", "/documents/" + otherDocID, [6]outcome{U, U, F, F, F, A}},
		{"GET", "/inbox", "/inbox", [6]outcome{U, U, A, A, A, A}},
		// fakeChecker allows every "read", so any authenticated caller can
		// read the audit log. A real policy would usually restrict it.
		{"GET", "/audit", "/audit", [6]outcome{U, U, A, A, A, A}},
		{"GET", "/audit/{id}", "/audit/" + otherDocID, [6]outcome{U, U, A, A, A, A}},
	}

	handler := newMatrixRouter(t)

	covered := map[string]bool{}
	for _, tt := range tests {
		covered[tt.method+" "+tt.route] = true
		for i, role := range matrixRoles {
			t.Run(tt.method+" "+tt.path+"/"+role.name, func(t *testing.T) {
				req := httptest.NewRequest(tt.method, tt.path, nil)
				if role.token != "" {
					req.Header.Set("Authorization", "Bearer "+role.token)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				if got := outcomeOf(rec.Code); got != tt.want[i] {
					t.Errorf("%s %s as %s = %d (%s), want %s",
						tt.method, tt.path, role.name, rec.Code, got, tt.want[i])
				}
			})
		}
	}

	// Every registered route must have a row, so a new route cannot ship
	// without someone deciding who may call it.
	mux, ok := handler.(chi.Routes)
	if !ok {
		t.Fatalf("NewRouter returned %T, which chi.Walk cannot inspect", handler)
	}
	err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if key := method + " " + route; !covered[key] {
			t.Errorf("%s has no row in the role matrix", key)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
}

// newMatrixRouter wires NewRouter the way main.go does -- fakeAuthenticator,
// fakeChecker, and an access.Guard whose owned_document_ids attribute comes
// from a resolver -- with the resolver reporting ownedDocID as owned by the
// "owner" user instead of querying DocumentStore.OwnedIDs.
func newMatrixRouter(t *testing.T) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics, err := vmw.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	checker := fakeChecker{}
	guard := access.New(checker, access.WithPrincipalAttributes(
		func(_ context.Context, p *auth.Principal) (map[string]any, error) {
			var ids []string
			if p.UserID == "owner" {
				ids = []string{ownedDocID}
			}
			return map[string]any{"owned_document_ids": ids}, nil
		},
	))
	return NewRouter(nil, nil, fakeAuthenticator{}, checker, guard, metrics, newIPRateLimiter(10_000, time.Minute), newFakeStorage("http://localhost").Handler(), nil, logger)
}

func outcomeOf(code int) outcome {
	switch code {
	case http.StatusUnauthorized:
		return unauthenticated
	case http.StatusForbidden:
		return forbidden
	default:
		return allowed
	}
}

// The matrix reads a 500 from a nil handler as "allowed"; make sure a guard
// rejection is never confused with it, i.e. that a 403 body really comes
// from vogel's access guard and not from somewhere else.
func TestNewRouter_RoleMatrixForbiddenIsTheGuard(t *testing.T) {
	handler := newMatrixRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/documents", nil)
	req.Header.Set("Authorization", "Bearer u1:alice:")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "FORBIDDEN") {
		t.Errorf("POST /documents as plain user = %d %s, want 403 FORBIDDEN", rec.Code, rec.Body.String())
	}
}
