package sendgrid

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kafeiih/vogel/notification"
)

// countingTransport counts the requests that go through it.
type countingTransport struct {
	base http.RoundTripper
	n    atomic.Int32
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.base.RoundTrip(r)
}

// WrapTransport wraps the transport every API call goes through, keeping the
// client's timeout.
func TestSend_GoesThroughWrapTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	var counting *countingTransport
	var gotBase http.RoundTripper
	cfg := Config{APIKey: "SG.test", HTTPTimeout: 7 * time.Second, WrapTransport: func(base http.RoundTripper) http.RoundTripper {
		gotBase = base
		counting = &countingTransport{base: base}
		return counting
	}}
	n, err := NewSendGridNotifier(cfg, "noreply@inst.cl", slog.Default())
	if err != nil {
		t.Fatalf("NewSendGridNotifier: %v", err)
	}
	n.requestTemplate.BaseURL = srv.URL + "/v3/mail/send"

	if err := n.Send(context.Background(), &notification.Message{To: "user@example.com", Subject: "s", Text: "t"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotBase != http.DefaultTransport {
		t.Errorf("WrapTransport base = %T, want http.DefaultTransport", gotBase)
	}
	if counting.n.Load() != 1 {
		t.Errorf("requests through the wrapper = %d, want 1", counting.n.Load())
	}
	if n.httpClient.Timeout != 7*time.Second {
		t.Errorf("httpClient.Timeout = %v, want 7s", n.httpClient.Timeout)
	}
}

func TestNewSendGridNotifier_NoWrapTransportKeepsTheDefault(t *testing.T) {
	n, err := NewSendGridNotifier(Config{APIKey: "SG.test"}, "", slog.Default())
	if err != nil {
		t.Fatalf("NewSendGridNotifier: %v", err)
	}
	if n.httpClient.Transport != nil {
		t.Errorf("Transport = %T, want nil (http.DefaultTransport)", n.httpClient.Transport)
	}
}
