package zitadel

import (
	"context"
	"strings"
	"testing"
)

// TestNew_IssuerURL_AuthenticatesEndToEnd proves New accepts the issuer as a
// URL, the way Config.Issuer documents it: a plain-HTTP URL with a port
// reaches the httptest issuer, and a token it signs authenticates.
func TestNew_IssuerURL_AuthenticatesEndToEnd(t *testing.T) {
	ti := newTestIssuer(t)
	a, err := New(context.Background(), Config{Issuer: ti.srv.URL, ClientID: "my-client"})
	if err != nil {
		t.Fatalf("New(Issuer: %q) = %v, want nil", ti.srv.URL, err)
	}
	token := ti.sign(t, map[string]any{"sub": "user-1", "aud": []string{"my-client"}})
	p, err := a.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("Authenticate = %v, want nil", err)
	}
	if p.UserID != "user-1" {
		t.Errorf("UserID = %q, want %q", p.UserID, "user-1")
	}
}

func TestIssuerProvider_Origin(t *testing.T) {
	tests := []struct {
		issuer string
		want   string
	}{
		{"https://my-instance.zitadel.cloud", "https://my-instance.zitadel.cloud"},
		{"https://my-instance.zitadel.cloud/", "https://my-instance.zitadel.cloud"},
		{"HTTPS://my-instance.zitadel.cloud", "https://my-instance.zitadel.cloud"},
		{"https://auth.example.com:8443", "https://auth.example.com:8443"},
		{"http://localhost:8080", "http://localhost:8080"},
		{"http://localhost", "http://localhost"},
		{"https://[::1]:8443", "https://[::1]:8443"},
		{"http://[::1]:8080", "http://[::1]:8080"},
		// Without a scheme the value is passed through as before.
		{"my-instance.zitadel.cloud", "https://my-instance.zitadel.cloud"},
		{"127.0.0.1:9000", "https://127.0.0.1:9000"},
	}
	for _, tt := range tests {
		t.Run(tt.issuer, func(t *testing.T) {
			z, err := issuerProvider(tt.issuer)
			if err != nil {
				t.Fatalf("issuerProvider(%q) = %v, want nil", tt.issuer, err)
			}
			if got := z.Origin(); got != tt.want {
				t.Errorf("Origin() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNew_InvalidIssuerURL_ReturnsError(t *testing.T) {
	tests := []struct {
		issuer  string
		wantErr string
	}{
		{"ftp://my-instance.zitadel.cloud", `scheme must be "https" or "http"`},
		{"https://", "has no host"},
		{"https://my-instance.zitadel.cloud/oauth", "must not have a path"},
		{"https://my-instance.zitadel.cloud?x=1", "must not have a query or fragment"},
		{"https://my-instance.zitadel.cloud#x", "must not have a query or fragment"},
		{"https://user:pass@my-instance.zitadel.cloud", "must not have user info"},
		{"https://my-instance.zitadel.cloud:notaport", "auth/zitadel: invalid issuer"},
		{"https://my-instance.zitadel.cloud:0", "port must be between 1 and 65535"},
		{"http://localhost:0", "port must be between 1 and 65535"},
	}
	for _, tt := range tests {
		t.Run(tt.issuer, func(t *testing.T) {
			_, err := New(context.Background(), Config{Issuer: tt.issuer, ClientID: "c"})
			if err == nil {
				t.Fatalf("New(Issuer: %q) = nil, want an error", tt.issuer)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("New(Issuer: %q) = %q, want it to contain %q", tt.issuer, err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "discovery") {
				t.Errorf("New(Issuer: %q) = %q, want it rejected before OIDC discovery", tt.issuer, err)
			}
		})
	}
}
