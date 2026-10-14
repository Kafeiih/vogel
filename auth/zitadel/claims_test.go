package zitadel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization/oauth"

	"github.com/kafeiih/vogel/auth"
)

func TestCheckClaims(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	const clientID = "client-1"

	tests := []struct {
		name        string
		claims      tokenClaims
		maxLifetime time.Duration
		wantErr     error
	}{
		{name: "azp absent is accepted", claims: tokenClaims{}},
		{name: "azp equal to the client ID is accepted", claims: tokenClaims{authorizedParty: clientID, hasAuthorizedParty: true}},
		{name: "azp present but empty is accepted", claims: tokenClaims{authorizedParty: "", hasAuthorizedParty: true}},
		{name: "azp of another client is rejected", claims: tokenClaims{authorizedParty: "other-client", hasAuthorizedParty: true}, wantErr: errAuthorizedParty},
		{name: "non-string azp is rejected", claims: tokenClaims{authorizedPartyMalformed: true, hasAuthorizedParty: true}, wantErr: errAuthorizedParty},
		{name: "nbf in the past is accepted", claims: tokenClaims{notBefore: now.Add(-time.Hour)}},
		{name: "nbf within the clock-skew leeway is accepted", claims: tokenClaims{notBefore: now.Add(notBeforeLeeway / 2)}},
		{name: "nbf in the future is rejected", claims: tokenClaims{notBefore: now.Add(time.Hour)}, wantErr: errNotYetValid},
		{
			name:        "lifetime under the maximum is accepted",
			claims:      tokenClaims{issuedAt: now.Add(-time.Minute), expiration: now.Add(59 * time.Minute)},
			maxLifetime: time.Hour,
		},
		{
			name:        "lifetime equal to the maximum is accepted",
			claims:      tokenClaims{issuedAt: now, expiration: now.Add(time.Hour)},
			maxLifetime: time.Hour,
		},
		{
			name:        "lifetime over the maximum is rejected",
			claims:      tokenClaims{issuedAt: now, expiration: now.Add(24 * time.Hour)},
			maxLifetime: time.Hour,
			wantErr:     errLifetimeExceeded,
		},
		{
			name:        "missing iat with a maximum set is rejected",
			claims:      tokenClaims{expiration: now.Add(time.Minute)},
			maxLifetime: time.Hour,
			wantErr:     errLifetimeExceeded,
		},
		{
			name:        "missing exp with a maximum set is rejected",
			claims:      tokenClaims{issuedAt: now},
			maxLifetime: time.Hour,
			wantErr:     errLifetimeExceeded,
		},
		{
			name:   "zero maximum skips the lifetime check",
			claims: tokenClaims{issuedAt: now, expiration: now.Add(365 * 24 * time.Hour)},
		},
		{
			name:   "zero maximum accepts a missing iat",
			claims: tokenClaims{expiration: now.Add(time.Minute)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkClaims(tt.claims, clientID, tt.maxLifetime, now)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("checkClaims() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("checkClaims() = %v, want wrapping %v", err, tt.wantErr)
			}
			if !errors.Is(err, errClaimsRejected) {
				t.Errorf("checkClaims() = %v, want wrapping errClaimsRejected", err)
			}
		})
	}
}

func TestClaimsOf_ReadsVerifiedClaims(t *testing.T) {
	iat := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	c := claimsOf(&oauth.IntrospectionContext{
		IntrospectionResponse: oidc.IntrospectionResponse{
			IssuedAt:   oidc.FromTime(iat),
			NotBefore:  oidc.FromTime(iat),
			Expiration: oidc.FromTime(iat.Add(time.Hour)),
			Claims:     map[string]any{"azp": "client-1"},
		},
	})
	if !c.hasAuthorizedParty || c.authorizedParty != "client-1" || c.authorizedPartyMalformed {
		t.Errorf("azp = (%q, present %v, malformed %v), want (client-1, true, false)", c.authorizedParty, c.hasAuthorizedParty, c.authorizedPartyMalformed)
	}
	if !c.issuedAt.Equal(iat) || !c.notBefore.Equal(iat) || !c.expiration.Equal(iat.Add(time.Hour)) {
		t.Errorf("times = (iat %v, nbf %v, exp %v), want (%v, %v, %v)", c.issuedAt, c.notBefore, c.expiration, iat, iat, iat.Add(time.Hour))
	}

	malformed := claimsOf(&oauth.IntrospectionContext{
		IntrospectionResponse: oidc.IntrospectionResponse{Claims: map[string]any{"azp": 42.0}},
	})
	if !malformed.hasAuthorizedParty || !malformed.authorizedPartyMalformed {
		t.Errorf("non-string azp: present %v, malformed %v, want both true", malformed.hasAuthorizedParty, malformed.authorizedPartyMalformed)
	}
}

// TestAuthenticate_RejectsClaimsTheSDKDoesNotCheck proves the post-SDK checks
// run on the real Authenticate path and surface as the same generic
// ErrUnauthenticated as any other invalid token.
func TestAuthenticate_RejectsClaimsTheSDKDoesNotCheck(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		resp    oidc.IntrospectionResponse
		cfg     Config
		wantErr error
	}{
		{
			name:    "azp of another client",
			resp:    oidc.IntrospectionResponse{Active: true, Subject: "user-1", Claims: map[string]any{"azp": "other-client"}},
			cfg:     Config{ClientID: "client-1"},
			wantErr: errAuthorizedParty,
		},
		{
			name:    "token not yet valid",
			resp:    oidc.IntrospectionResponse{Active: true, Subject: "user-1", NotBefore: oidc.FromTime(now.Add(time.Hour))},
			cfg:     Config{ClientID: "client-1"},
			wantErr: errNotYetValid,
		},
		{
			name: "lifetime over the configured maximum",
			resp: oidc.IntrospectionResponse{
				Active: true, Subject: "user-1",
				IssuedAt:   oidc.FromTime(now),
				Expiration: oidc.FromTime(now.Add(48 * time.Hour)),
			},
			cfg:     Config{ClientID: "client-1", MaxTokenLifetime: time.Hour},
			wantErr: errLifetimeExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAuthenticatorWithConfig(t, &fakeVerifier{ctx: &oauth.IntrospectionContext{IntrospectionResponse: tt.resp}}, tt.cfg)
			p, err := a.Authenticate(context.Background(), "sometoken")
			if p != nil {
				t.Errorf("principal = %+v, want nil", p)
			}
			if !errors.Is(err, auth.ErrUnauthenticated) {
				t.Errorf("error = %v, want wrapping auth.ErrUnauthenticated", err)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want wrapping %v", err, tt.wantErr)
			}
		})
	}
}

func TestAuthenticate_AcceptsMatchingAzp(t *testing.T) {
	resp := oidc.IntrospectionResponse{Active: true, Subject: "user-1", Claims: map[string]any{"azp": "client-1"}}
	a := newTestAuthenticatorWithConfig(t, &fakeVerifier{ctx: &oauth.IntrospectionContext{IntrospectionResponse: resp}}, Config{ClientID: "client-1"})
	p, err := a.Authenticate(context.Background(), "sometoken")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.UserID != "user-1" {
		t.Errorf("UserID = %q, want user-1", p.UserID)
	}
}

func TestNew_NegativeMaxTokenLifetime_ReturnsError(t *testing.T) {
	_, err := New(context.Background(), Config{Issuer: "auth.example.invalid", ClientID: "c", MaxTokenLifetime: -time.Second})
	if err == nil {
		t.Fatal("expected an error for a negative MaxTokenLifetime, got nil")
	}
}
