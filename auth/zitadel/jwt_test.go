package zitadel

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"

	"github.com/kafeiih/vogel/auth"
)

// testIssuer is an in-process OIDC issuer: it serves discovery and a JWKS
// with one RSA key, and signs RS256 access tokens with that key, so the
// SDK's real local verification (signature, iss, exp, aud) runs end to end
// without any external network.
type testIssuer struct {
	srv *httptest.Server
	key *rsa.PrivateKey
}

const testKeyID = "test-key"

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	ti := &testIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   ti.srv.URL,
			"jwks_uri": ti.srv.URL + "/keys",
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		b64 := base64.RawURLEncoding
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": testKeyID,
				"n":   b64.EncodeToString(key.N.Bytes()),
				"e":   b64.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}},
		})
	})
	ti.srv = httptest.NewServer(mux)
	t.Cleanup(ti.srv.Close)
	return ti
}

// sign returns a compact RS256 JWT carrying claims plus iss and a one-hour
// iat/exp window.
func (ti *testIssuer) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	signingInput := ti.signingInput(t, "RS256", claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, ti.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// signingInput returns the encoded header and payload of a JWT whose header
// names alg, carrying claims plus iss and a one-hour iat/exp window.
func (ti *testIssuer) signingInput(t *testing.T, alg string, claims map[string]any) string {
	t.Helper()
	now := time.Now()
	payload := map[string]any{
		"iss": ti.srv.URL,
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	for k, v := range claims {
		payload[k] = v
	}
	b64 := base64.RawURLEncoding
	header, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": testKeyID})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b64.EncodeToString(header) + "." + b64.EncodeToString(body)
}

// authenticator builds an Authenticator around the SDK's real JWT verifier
// pointed at this issuer. zitadel.New is given the plain-HTTP host and port
// (WithInsecure) because the httptest server has no TLS; everything after
// discovery is the same code path New uses.
func (ti *testIssuer) authenticator(t *testing.T, cfg Config) *Authenticator {
	t.Helper()
	return ti.authenticatorAccepting(t, cfg, acceptedSigningAlgorithms())
}

// authenticatorAccepting is authenticator with the verifier limited to algs.
func (ti *testIssuer) authenticatorAccepting(t *testing.T, cfg Config, algs []string) *Authenticator {
	t.Helper()
	u, err := url.Parse(ti.srv.URL)
	if err != nil {
		t.Fatalf("parse issuer URL: %v", err)
	}
	z := zitadel.New(u.Hostname(), zitadel.WithInsecure(u.Port()))
	az, err := authorization.New(context.Background(), z, withJWT(cfg.ClientID, issuerHTTPClient(cfg), algs))
	if err != nil {
		t.Fatalf("authorization.New: %v", err)
	}
	return newFromAuthorizer(az, cfg, nil)
}

// TestAuthenticate_VerifiedJWT_AzpPolicy proves that azp from a really
// verified JWT reaches IntrospectionContext.Claims (AccessTokenClaims decodes
// the whole payload into its Claims map, which oauth.JWTVerification copies)
// and that the opt-in allowlist is enforced on it.
func TestAuthenticate_VerifiedJWT_AzpPolicy(t *testing.T) {
	ti := newTestIssuer(t)
	tests := []struct {
		name    string
		claims  map[string]any
		allowed []string
		wantErr error
	}{
		{
			name:   "frontend azp without an allowlist is accepted",
			claims: map[string]any{"sub": "user-1", "aud": []string{"api-client", "spa-client"}, "azp": "spa-client"},
		},
		{
			name:    "frontend azp in the allowlist is accepted",
			claims:  map[string]any{"sub": "user-1", "aud": []string{"api-client"}, "azp": "spa-client"},
			allowed: []string{"spa-client"},
		},
		{
			name:    "azp outside the allowlist is rejected",
			claims:  map[string]any{"sub": "user-1", "aud": []string{"api-client"}, "azp": "other-client"},
			allowed: []string{"spa-client"},
			wantErr: errAuthorizedParty,
		},
		{
			name:    "missing azp with an allowlist is rejected",
			claims:  map[string]any{"sub": "user-1", "aud": []string{"api-client"}},
			allowed: []string{"spa-client"},
			wantErr: errAuthorizedParty,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := ti.authenticator(t, Config{ClientID: "api-client", AllowedAuthorizedParties: tt.allowed})
			p, err := a.Authenticate(context.Background(), ti.sign(t, tt.claims))
			if tt.wantErr != nil {
				if !errors.Is(err, auth.ErrUnauthenticated) || !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want wrapping auth.ErrUnauthenticated and %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.UserID != "user-1" {
				t.Errorf("UserID = %q, want user-1", p.UserID)
			}
		})
	}
}

// TestAuthenticate_VerifiedJWT_ExposesAzpInClaims pins the SDK behavior
// claimsOf relies on: azp has no typed field in IntrospectionResponse and is
// read from the Claims map.
func TestAuthenticate_VerifiedJWT_ExposesAzpInClaims(t *testing.T) {
	ti := newTestIssuer(t)
	a := ti.authenticator(t, Config{ClientID: "api-client"})
	authCtx, err := a.authorizer.CheckAuthorization(context.Background(), "Bearer "+ti.sign(t, map[string]any{
		"sub": "user-1", "aud": []string{"api-client"}, "azp": "spa-client",
	}))
	if err != nil {
		t.Fatalf("CheckAuthorization: %v", err)
	}
	if got := authCtx.Claims["azp"]; got != "spa-client" {
		t.Errorf(`Claims["azp"] = %v, want spa-client`, got)
	}
}

// TestAuthenticate_VerifiedJWT_RejectsUnpinnedAlgorithms proves the verifier
// only accepts acceptedSigningAlgorithms: an unsigned token (alg none) and an
// HS256 token keyed with the issuer's public RSA modulus (the classic
// algorithm-confusion attack) are both refused before any claim is trusted.
// The same authenticator accepts a correctly signed RS256 token, so the pin
// is proven in both directions and not only through the SDK's error text.
func TestAuthenticate_VerifiedJWT_RejectsUnpinnedAlgorithms(t *testing.T) {
	ti := newTestIssuer(t)
	a := ti.authenticator(t, Config{ClientID: "api-client"})
	claims := map[string]any{"sub": "user-1", "aud": []string{"api-client"}}
	b64 := base64.RawURLEncoding

	if _, err := a.Authenticate(context.Background(), ti.sign(t, claims)); err != nil {
		t.Fatalf("RS256 control token: unexpected error: %v", err)
	}

	hsInput := ti.signingInput(t, "HS256", claims)
	mac := hmac.New(sha256.New, ti.key.N.Bytes())
	mac.Write([]byte(hsInput))

	tokens := map[string]string{
		"none":  ti.signingInput(t, "none", claims) + ".",
		"HS256": hsInput + "." + b64.EncodeToString(mac.Sum(nil)),
	}
	for alg, token := range tokens {
		t.Run(alg, func(t *testing.T) {
			// The SDK flattens its error chain, so the reason is matched on
			// the message rather than with errors.Is.
			_, err := a.Authenticate(context.Background(), token)
			if !errors.Is(err, auth.ErrUnauthenticated) || !strings.Contains(err.Error(), oidc.ErrSignatureUnsupportedAlg.Error()) {
				t.Fatalf("error = %v, want wrapping auth.ErrUnauthenticated with reason %q", err, oidc.ErrSignatureUnsupportedAlg)
			}
		})
	}
}

// TestAcceptedSigningAlgorithms pins the accepted list so a change to it is a
// deliberate, reviewed decision rather than an SDK default drifting.
func TestAcceptedSigningAlgorithms(t *testing.T) {
	want := []string{"RS256", "ES256", "PS256"}
	if got := acceptedSigningAlgorithms(); !slices.Equal(got, want) {
		t.Errorf("acceptedSigningAlgorithms() = %v, want %v", got, want)
	}
}

// TestWithJWT_HonorsAcceptedSigningAlgorithms proves withJWT really hands its
// algorithm list to the SDK verifier: acceptedSigningAlgorithms equals the
// SDK default, so only a narrower list shows the option takes effect.
func TestWithJWT_HonorsAcceptedSigningAlgorithms(t *testing.T) {
	ti := newTestIssuer(t)
	a := ti.authenticatorAccepting(t, Config{ClientID: "api-client"}, []string{"ES256"})
	_, err := a.Authenticate(context.Background(), ti.sign(t, map[string]any{"sub": "user-1", "aud": []string{"api-client"}}))
	if !errors.Is(err, auth.ErrUnauthenticated) || !strings.Contains(err.Error(), oidc.ErrSignatureUnsupportedAlg.Error()) {
		t.Fatalf("error = %v, want an RS256 token refused when only ES256 is accepted", err)
	}
}

// TestAuthenticate_VerifiedJWT_RejectsFutureIssuedAt proves a really verified
// JWT whose iat lies beyond the clock-skew leeway is refused, even with no
// MaxTokenLifetime configured.
func TestAuthenticate_VerifiedJWT_RejectsFutureIssuedAt(t *testing.T) {
	ti := newTestIssuer(t)
	a := ti.authenticator(t, Config{ClientID: "api-client"})
	future := time.Now().Add(time.Hour)
	_, err := a.Authenticate(context.Background(), ti.sign(t, map[string]any{
		"sub": "user-1", "aud": []string{"api-client"},
		"iat": future.Unix(), "exp": future.Add(time.Hour).Unix(),
	}))
	if !errors.Is(err, auth.ErrUnauthenticated) || !errors.Is(err, errIssuedInFuture) {
		t.Fatalf("error = %v, want wrapping auth.ErrUnauthenticated and errIssuedInFuture", err)
	}
}
