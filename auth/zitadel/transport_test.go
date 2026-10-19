package zitadel

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

type countingTransport struct {
	base http.RoundTripper
	n    atomic.Int32
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.base.RoundTrip(r)
}

// Discovery and JWKS fetches go through Config.WrapTransport, with the
// client's timeout kept.
func TestWrapTransport_CarriesTheIssuerCalls(t *testing.T) {
	ti := newTestIssuer(t)
	counting := &countingTransport{}
	cfg := Config{ClientID: "api-client", WrapTransport: func(base http.RoundTripper) http.RoundTripper {
		counting.base = base
		return counting
	}}
	a := ti.authenticator(t, cfg)

	if _, err := a.Authenticate(context.Background(), ti.sign(t, map[string]any{"sub": "user-1", "aud": []string{"api-client"}})); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if counting.base != http.DefaultTransport {
		t.Errorf("WrapTransport base = %T, want http.DefaultTransport", counting.base)
	}
	if counting.n.Load() < 2 {
		t.Errorf("requests through the wrapper = %d, want discovery and JWKS", counting.n.Load())
	}
	if c := issuerHTTPClient(cfg); c.Timeout != DefaultHTTPTimeout || c.Transport != counting {
		t.Errorf("issuerHTTPClient = (timeout %v, transport %T), want (%v, the wrapper)", c.Timeout, c.Transport, DefaultHTTPTimeout)
	}
}

func TestIssuerHTTPClient_NoWrapTransportKeepsTheDefault(t *testing.T) {
	if c := issuerHTTPClient(Config{}); c.Transport != nil {
		t.Errorf("Transport = %T, want nil (http.DefaultTransport)", c.Transport)
	}
}
