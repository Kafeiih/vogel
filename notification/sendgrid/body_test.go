package sendgrid

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kafeiih/vogel/notification"
)

// truncatedBodyServer answers with the given status and a Content-Length
// larger than what it writes, then drops the connection, so the client's
// body read fails after the status line was received.
func truncatedBodyServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Message-Id", "msg-123")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("partial"))
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSend_AcceptedDespiteBodyReadError proves a 2xx is reported as sent even
// if reading the response body fails: SendGrid already accepted the email,
// and an error would make a retrying caller send it twice.
func TestSend_AcceptedDespiteBodyReadError(t *testing.T) {
	srv := truncatedBodyServer(t, http.StatusAccepted)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	n, err := NewSendGridNotifier(Config{APIKey: "SG.test"}, "noreply@inst.cl", logger)
	if err != nil {
		t.Fatalf("NewSendGridNotifier: %v", err)
	}
	n.requestTemplate.BaseURL = srv.URL + "/v3/mail/send"

	const to = "juana.perez@example.com"
	if err := n.Send(context.Background(), &notification.Message{To: to, Subject: "s", Text: "t"}); err != nil {
		t.Fatalf("Send = %v, want nil after a 202", err)
	}

	out := buf.String()
	if !strings.Contains(out, "email sent via SendGrid") || !strings.Contains(out, `"message_id":"msg-123"`) {
		t.Errorf("success log with the message ID not emitted; got %q", out)
	}
	if strings.Contains(out, to) {
		t.Errorf("log contains the recipient; got %q", out)
	}
	// The Warn is the only signal for this path; asserting it also proves the
	// truncated body really made the read fail, so the test exercises the
	// read-error branch rather than a clean 202.
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "reading SendGrid response body failed after the email was accepted") {
		t.Errorf("body-read failure Warn not emitted; got %q", out)
	}
}

// TestSend_ErrorStatusWithBodyReadError_Fails proves the 2xx leniency does
// not turn an API error into a success.
func TestSend_ErrorStatusWithBodyReadError_Fails(t *testing.T) {
	srv := truncatedBodyServer(t, http.StatusBadRequest)

	n, err := NewSendGridNotifier(Config{APIKey: "SG.test"}, "noreply@inst.cl", slog.Default())
	if err != nil {
		t.Fatalf("NewSendGridNotifier: %v", err)
	}
	n.requestTemplate.BaseURL = srv.URL + "/v3/mail/send"

	if err := n.Send(context.Background(), &notification.Message{To: "user@example.com", Subject: "s", Text: "t"}); err == nil {
		t.Fatal("Send = nil after a 400, want an error")
	}
}
