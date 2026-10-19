package s3

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
)

type countingTransport struct {
	base http.RoundTripper
	n    atomic.Int32
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.base.RoundTrip(r)
}

// WrapTransport wraps the SDK's own client, which keeps vogel's bounds
// (response headers, dial and TLS handshake) and no client timeout.
func TestHTTPClientFor_WrapTransportKeepsTheBoundedSDKClient(t *testing.T) {
	var base http.RoundTripper
	cfg := Config{WrapTransport: func(b http.RoundTripper) http.RoundTripper {
		base = b
		return b
	}}
	httpClientFor(cfg, 5*time.Second)

	d, ok := base.(clientRoundTripper)
	if !ok {
		t.Fatalf("WrapTransport base = %T, want clientRoundTripper", base)
	}
	b, ok := d.client.(*awshttp.BuildableClient)
	if !ok {
		t.Fatalf("wrapped client = %T, want *awshttp.BuildableClient", d.client)
	}
	if got := b.GetTransport().ResponseHeaderTimeout; got != 5*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want 5s", got)
	}
	if got := b.GetTimeout(); got != 0 {
		t.Errorf("client Timeout = %v, want 0 (body streaming must not be capped)", got)
	}
}

// Requests go through the wrapper, and the SDK's redirect policy still
// applies: a 301 is returned to the SDK, not followed.
func TestHTTPClientFor_WrapTransportKeepsTheSDKRedirectPolicy(t *testing.T) {
	var followed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			followed.Store(true)
			return
		}
		http.Redirect(w, r, "/moved", http.StatusMovedPermanently)
	}))
	t.Cleanup(srv.Close)

	counting := &countingTransport{}
	c := httpClientFor(Config{WrapTransport: func(b http.RoundTripper) http.RoundTripper {
		counting.base = b
		return counting
	}}, 5*time.Second)

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/bucket/key", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusMovedPermanently || followed.Load() {
		t.Errorf("status = %d, followed = %v; want the 301 returned unfollowed", resp.StatusCode, followed.Load())
	}
	if counting.n.Load() != 1 {
		t.Errorf("requests through the wrapper = %d, want 1", counting.n.Load())
	}
}

func TestHTTPClientFor_NoWrapTransportKeepsTheBuildableClient(t *testing.T) {
	if c, ok := httpClientFor(Config{}, time.Second).(*awshttp.BuildableClient); !ok {
		t.Errorf("httpClientFor without WrapTransport = %T, want *awshttp.BuildableClient", c)
	}
}
