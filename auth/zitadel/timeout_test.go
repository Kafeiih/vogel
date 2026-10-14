package zitadel

import (
	"context"
	"net"
	"net/http"
	"sync"
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

// TestNew_StalledIssuer_FailsWithinHTTPTimeout proves New's OIDC discovery
// goes through the bounded client end to end. The issuer accepts TCP
// connections and never answers, so the TLS handshake of the discovery
// request stalls; with a context that has no deadline only
// Config.HTTPTimeout can end it. http.DefaultClient would wait for the
// default transport's 10s TLS handshake timeout instead.
func TestNew_StalledIssuer_FailsWithinHTTPTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c) // held open, never written to
			mu.Unlock()
		}
	}()

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := New(context.Background(), Config{Issuer: ln.Addr().String(), ClientID: "c", HTTPTimeout: 200 * time.Millisecond})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("New = nil against a stalled issuer, want a discovery error")
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("New took %v, want it bounded by the 200ms HTTPTimeout", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("New did not return within 5s: discovery is not using the bounded client")
	}
}
