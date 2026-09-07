package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kafeiih/vogel/auth"
)

// fakeAuthenticator implements auth.Authenticator with a canned result, so
// Authenticate's status mapping and fail-closed guarantee can be tested
// without a real identity provider.
type fakeAuthenticator struct {
	principal *auth.Principal
	err       error
}

func (f *fakeAuthenticator) Authenticate(context.Context, *http.Request) (*auth.Principal, error) {
	return f.principal, f.err
}

func TestAuthenticate_Success_SetsPrincipalAndCallsNext(t *testing.T) {
	want := &auth.Principal{UserID: "u1", Username: "alice", Roles: []string{"admin"}}
	fa := &fakeAuthenticator{principal: want}

	var got *auth.Principal
	handler := Authenticate(fa, slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = auth.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got == nil || got.UserID != "u1" {
		t.Fatalf("principal not propagated into context: %+v", got)
	}
}

func TestAuthenticate_Unauthenticated_Returns401AndDoesNotCallNext(t *testing.T) {
	fa := &fakeAuthenticator{err: auth.ErrUnauthenticated}
	called := false
	handler := Authenticate(fa, slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("next.ServeHTTP was called despite an authentication failure (fail-open)")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuthenticate_UnrecognizedError_Returns401(t *testing.T) {
	// Any error the Authenticator returns that isn't one of the recognized
	// sentinels must still fail closed as 401, never leak through as a 200.
	fa := &fakeAuthenticator{err: fmt.Errorf("boom")}
	called := false
	handler := Authenticate(fa, slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("next.ServeHTTP was called despite an unrecognized authentication error (fail-open)")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuthenticate_Forbidden_Returns403AndDoesNotCallNext(t *testing.T) {
	fa := &fakeAuthenticator{err: fmt.Errorf("wrapped: %w", auth.ErrForbidden)}
	called := false
	handler := Authenticate(fa, slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("next.ServeHTTP was called despite an authentication failure (fail-open)")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAuthenticate_ServiceUnavailable_Returns503AndDoesNotCallNext(t *testing.T) {
	fa := &fakeAuthenticator{err: fmt.Errorf("idp down: %w", auth.ErrServiceUnavailable)}
	called := false
	handler := Authenticate(fa, slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("next.ServeHTTP was called despite a provider outage (fail-open)")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d (a provider outage must not surface as 401/403/500 — see FIX 2)", rec.Code, http.StatusServiceUnavailable)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	msg, _ := body["message"].(string)
	if msg != DefaultAuthMessages().ServiceUnavailable {
		t.Errorf("message = %q, want default neutral message %q", msg, DefaultAuthMessages().ServiceUnavailable)
	}
}

func TestAuthenticate_CustomMessages_OverrideDefaults(t *testing.T) {
	fa := &fakeAuthenticator{err: auth.ErrUnauthenticated}
	custom := "Se requiere autenticación"
	handler := Authenticate(fa, slog.Default(), WithAuthMessages(AuthMessages{Unauthorized: custom}))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if body["message"] != custom {
		t.Errorf("message = %v, want overridden message %q", body["message"], custom)
	}
}
