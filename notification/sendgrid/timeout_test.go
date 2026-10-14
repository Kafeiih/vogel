package sendgrid

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kafeiih/vogel/notification"
)

func TestNewSendGridNotifier_HTTPTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{name: "zero uses the default", timeout: 0, want: DefaultHTTPTimeout},
		{name: "explicit override", timeout: 5 * time.Second, want: 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := NewSendGridNotifier(Config{APIKey: "SG.test", HTTPTimeout: tt.timeout}, "", slog.Default())
			if err != nil {
				t.Fatalf("NewSendGridNotifier: %v", err)
			}
			if n.httpClient == nil || n.httpClient == http.DefaultClient {
				t.Fatalf("httpClient = %v, want a dedicated client", n.httpClient)
			}
			if got := n.httpClient.Timeout; got != tt.want {
				t.Errorf("httpClient.Timeout = %v, want %v", got, tt.want)
			}
		})
	}
	if DefaultHTTPTimeout != 30*time.Second {
		t.Errorf("DefaultHTTPTimeout = %v, want 30s", DefaultHTTPTimeout)
	}
}

func TestNewSendGridNotifier_NegativeHTTPTimeout_ReturnsError(t *testing.T) {
	if _, err := NewSendGridNotifier(Config{APIKey: "SG.test", HTTPTimeout: -time.Second}, "", slog.Default()); err == nil {
		t.Fatal("expected an error for a negative HTTPTimeout, got nil")
	}
}

// TestSend_HonoursHTTPTimeout proves Send goes through the notifier's own
// bounded client: a server that never answers makes Send fail after the
// timeout instead of hanging, even with a context that has no deadline.
func TestSend_HonoursHTTPTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	n, err := NewSendGridNotifier(Config{APIKey: "SG.test", HTTPTimeout: 50 * time.Millisecond}, "noreply@inst.cl", slog.Default())
	if err != nil {
		t.Fatalf("NewSendGridNotifier: %v", err)
	}
	n.client.BaseURL = srv.URL + "/v3/mail/send"

	done := make(chan error, 1)
	go func() {
		done <- n.Send(context.Background(), &notification.Message{To: "user@example.com", Subject: "s", Text: "t"})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Send returned nil against a server that never answers, want a timeout error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return within 5s: the HTTP client has no timeout")
	}
}
