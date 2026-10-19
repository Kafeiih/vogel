package s3

import (
	"net/http"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
)

type wrappedTransport struct{ base http.RoundTripper }

func (w *wrappedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return w.base.RoundTrip(r)
}

// WrapTransport receives the transport vogel bounds (response headers, dial
// and TLS handshake), and the client uses what it returns.
func TestHTTPClientFor_WrapTransport(t *testing.T) {
	var got *wrappedTransport
	cfg := Config{WrapTransport: func(base http.RoundTripper) http.RoundTripper {
		got = &wrappedTransport{base: base}
		return got
	}}

	c, ok := httpClientFor(cfg, 5*time.Second).(*http.Client)
	if !ok {
		t.Fatalf("httpClientFor with WrapTransport = %T, want *http.Client", httpClientFor(cfg, 5*time.Second))
	}
	if c.Transport != got {
		t.Fatalf("client Transport = %T, want the wrapper", c.Transport)
	}
	base, ok := got.base.(*http.Transport)
	if !ok || base.ResponseHeaderTimeout != 5*time.Second {
		t.Fatalf("wrapped base = %T (ResponseHeaderTimeout %v), want vogel's bounded *http.Transport", got.base, base.ResponseHeaderTimeout)
	}
	if c.Timeout != 0 {
		t.Errorf("client Timeout = %v, want 0 (body streaming must not be capped)", c.Timeout)
	}
}

func TestHTTPClientFor_NoWrapTransportKeepsTheBuildableClient(t *testing.T) {
	if _, ok := httpClientFor(Config{}, time.Second).(*awshttp.BuildableClient); !ok {
		t.Errorf("httpClientFor without WrapTransport = %T, want *awshttp.BuildableClient", httpClientFor(Config{}, time.Second))
	}
}
