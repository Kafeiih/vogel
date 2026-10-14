package zitadel

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestJWKSHTTPClient_Timeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{name: "zero uses the default", timeout: 0, want: DefaultHTTPTimeout},
		{name: "explicit override", timeout: 3 * time.Second, want: 3 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := jwksHTTPClient(Config{HTTPTimeout: tt.timeout})
			if c == nil || c == http.DefaultClient {
				t.Fatalf("client = %v, want a dedicated client (http.DefaultClient has no timeout)", c)
			}
			if c.Timeout != tt.want {
				t.Errorf("Timeout = %v, want %v", c.Timeout, tt.want)
			}
		})
	}
	if DefaultHTTPTimeout != 10*time.Second {
		t.Errorf("DefaultHTTPTimeout = %v, want 10s", DefaultHTTPTimeout)
	}
}

func TestNew_NegativeHTTPTimeout_ReturnsError(t *testing.T) {
	_, err := New(context.Background(), Config{Issuer: "auth.example.invalid", ClientID: "c", HTTPTimeout: -time.Second})
	if err == nil {
		t.Fatal("expected an error for a negative HTTPTimeout, got nil")
	}
}
