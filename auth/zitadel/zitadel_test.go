package zitadel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization/oauth"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"

	"github.com/kafeiih/vogel/auth"
)

// fakeVerifier implements authorization.Verifier[*oauth.IntrospectionContext]
// with a canned response, so the real error-mapping and claim-extraction
// logic in Authenticate can be exercised without a network call or a real
// Zitadel instance.
type fakeVerifier struct {
	ctx *oauth.IntrospectionContext
	err error
}

func (f *fakeVerifier) CheckAuthorization(_ context.Context, _ string) (*oauth.IntrospectionContext, error) {
	return f.ctx, f.err
}

// newTestAuthenticator builds an Authenticator around v without ever
// performing OIDC discovery, a JWKS fetch, or any other network call:
// zitadel.New only builds a plain struct, and authorization.New calls the
// supplied initializer directly instead of dialing out.
func newTestAuthenticator(t *testing.T, v *fakeVerifier) *Authenticator {
	t.Helper()
	az, err := authorization.New(
		context.Background(),
		zitadel.New("https://example.zitadel.cloud"),
		func(context.Context, *zitadel.Zitadel) (authorization.Verifier[*oauth.IntrospectionContext], error) {
			return v, nil
		},
	)
	if err != nil {
		t.Fatalf("authorization.New: %v", err)
	}
	return newFromAuthorizer(az)
}

func TestAuthenticate_ValidToken_ReturnsPrincipal(t *testing.T) {
	authCtx := &oauth.IntrospectionContext{
		IntrospectionResponse: oidc.IntrospectionResponse{
			Active:  true,
			Subject: "user-1",
			Claims: map[string]any{
				"urn:zitadel:iam:org:project:roles": map[string]any{
					"admin": map[string]any{"org-1": "Org 1"},
				},
			},
		},
	}
	authCtx.PreferredUsername = "alice"

	a := newTestAuthenticator(t, &fakeVerifier{ctx: authCtx})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(authorization.HeaderName, "Bearer sometoken")

	p, err := a.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.UserID != "user-1" {
		t.Errorf("UserID = %q, want %q", p.UserID, "user-1")
	}
	if p.Username != "alice" {
		t.Errorf("Username = %q, want %q (preferred_username should win over legacy Username)", p.Username, "alice")
	}
	if len(p.Roles) != 1 || p.Roles[0] != "admin" {
		t.Errorf("Roles = %v, want [admin]", p.Roles)
	}
}

func TestAuthenticate_UsernameFallback_WhenNoPreferredUsername(t *testing.T) {
	authCtx := &oauth.IntrospectionContext{
		IntrospectionResponse: oidc.IntrospectionResponse{
			Active:   true,
			Subject:  "user-1",
			Username: "legacy-name",
		},
	}

	a := newTestAuthenticator(t, &fakeVerifier{ctx: authCtx})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(authorization.HeaderName, "Bearer sometoken")

	p, err := a.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Username != "legacy-name" {
		t.Errorf("Username = %q, want fallback %q", p.Username, "legacy-name")
	}
}

func TestAuthenticate_InvalidToken_ReturnsErrUnauthenticated(t *testing.T) {
	a := newTestAuthenticator(t, &fakeVerifier{err: errors.New("signature verification failed")})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(authorization.HeaderName, "Bearer badtoken")

	_, err := a.Authenticate(context.Background(), req)
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("error = %v, want wrapping auth.ErrUnauthenticated", err)
	}
	if errors.Is(err, auth.ErrServiceUnavailable) {
		t.Error("an invalid token must not map to ErrServiceUnavailable")
	}
}

func TestAuthenticate_MissingHeader_ReturnsErrUnauthenticated(t *testing.T) {
	a := newTestAuthenticator(t, &fakeVerifier{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No Authorization header set at all.

	_, err := a.Authenticate(context.Background(), req)
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("error = %v, want wrapping auth.ErrUnauthenticated", err)
	}
}

func TestAuthenticate_ProviderOutage_ReturnsErrServiceUnavailable(t *testing.T) {
	// The Zitadel SDK's Authorizer.CheckAuthorization classifies a verifier
	// error as a 5xx server error when its message contains a 5xx status
	// code, and wraps it as *authorization.ServiceUnavailableErr accordingly.
	a := newTestAuthenticator(t, &fakeVerifier{err: errors.New("introspection request failed: received 503 from server")})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(authorization.HeaderName, "Bearer sometoken")

	_, err := a.Authenticate(context.Background(), req)
	if !errors.Is(err, auth.ErrServiceUnavailable) {
		t.Errorf("error = %v, want wrapping auth.ErrServiceUnavailable", err)
	}
	if errors.Is(err, auth.ErrUnauthenticated) {
		t.Error("a provider outage must not map to ErrUnauthenticated (see FIX 2)")
	}
}

func TestMapError_PermissionDenied_ReturnsErrForbidden(t *testing.T) {
	err := mapError(authorization.NewErrorPermissionDenied(errors.New("missing required role")))
	if !errors.Is(err, auth.ErrForbidden) {
		t.Errorf("error = %v, want wrapping auth.ErrForbidden", err)
	}
}
