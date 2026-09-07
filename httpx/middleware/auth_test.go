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

	// gotToken records the token passed to Authenticate, so tests can assert
	// the middleware strips the "Bearer " scheme and surrounding whitespace
	// before calling the Authenticator.
	gotToken string
	called   bool
}

func (f *fakeAuthenticator) Authenticate(_ context.Context, token string) (*auth.Principal, error) {
	f.called = true
	f.gotToken = token
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
	req.Header.Set("Authorization", "Bearer sometoken")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got == nil || got.UserID != "u1" {
		t.Fatalf("principal not propagated into context: %+v", got)
	}
	if fa.gotToken != "sometoken" {
		t.Errorf("token passed to Authenticate = %q, want %q (Bearer scheme must be stripped)", fa.gotToken, "sometoken")
	}
}

func TestAuthenticate_Unauthenticated_Returns401AndDoesNotCallNext(t *testing.T) {
	fa := &fakeAuthenticator{err: auth.ErrUnauthenticated}
	called := false
	handler := Authenticate(fa, slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sometoken")
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
	req.Header.Set("Authorization", "Bearer sometoken")
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
	req.Header.Set("Authorization", "Bearer sometoken")
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
	req.Header.Set("Authorization", "Bearer sometoken")
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
	req.Header.Set("Authorization", "Bearer sometoken")
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

// TestAuthenticate_CredentialExtraction covers every malformed-credential
// shape that must be rejected as 401 by the middleware itself, without ever
// reaching the Authenticator — these failure modes moved here from the
// Authenticator's responsibility when Authenticate started taking a bare
// token string instead of *http.Request.
func TestAuthenticate_CredentialExtraction(t *testing.T) {
	tests := []struct {
		name   string
		header string // "" means: do not set the header at all
	}{
		{name: "no Authorization header at all"},
		{name: "empty header value", header: ""},
		{name: "wrong scheme (Basic)", header: "Basic dXNlcjpwYXNz"},
		{name: "Bearer with no token", header: "Bearer"},
		{name: "Bearer with no token but trailing space", header: "Bearer "},
		{name: "Bearer with only whitespace as token", header: "Bearer    "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fa := &fakeAuthenticator{principal: &auth.Principal{UserID: "u1"}}
			handler := Authenticate(fa, slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("next.ServeHTTP was called despite a malformed credential (fail-open)")
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.name != "no Authorization header at all" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if fa.called {
				t.Error("the Authenticator was called despite a malformed credential; extraction must reject it first")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

// TestAuthenticate_CredentialExtraction_ToleratesCaseAndWhitespace verifies
// the extraction accepts a lowercase "bearer" scheme (case-insensitive per
// RFC 6750/RFC 7235) and irregular whitespace around the scheme and the
// token, passing through only the trimmed token.
func TestAuthenticate_CredentialExtraction_ToleratesCaseAndWhitespace(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		wantToken string
	}{
		{name: "lowercase bearer", header: "bearer sometoken", wantToken: "sometoken"},
		{name: "mixed-case BeArEr", header: "BeArEr sometoken", wantToken: "sometoken"},
		{name: "extra whitespace around scheme and token", header: "  Bearer   sometoken  ", wantToken: "sometoken"},
		{name: "tab between scheme and token", header: "Bearer\tsometoken", wantToken: "sometoken"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fa := &fakeAuthenticator{principal: &auth.Principal{UserID: "u1"}}
			handler := Authenticate(fa, slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", tt.header)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if !fa.called {
				t.Fatal("the Authenticator was not called for a well-formed credential")
			}
			if fa.gotToken != tt.wantToken {
				t.Errorf("token passed to Authenticate = %q, want %q", fa.gotToken, tt.wantToken)
			}
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
			}
		})
	}
}
