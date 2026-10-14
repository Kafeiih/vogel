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

// TestSend_DoesNotLogPersonalData asserts the success log carries neither
// the recipient address nor the subject: both are personal data (a subject
// often names the person or their case) and must not reach logs.
func TestSend_DoesNotLogPersonalData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Message-Id", "msg-123")
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	n, err := NewSendGridNotifier(Config{APIKey: "SG.test"}, "noreply@inst.cl", logger)
	if err != nil {
		t.Fatalf("NewSendGridNotifier: %v", err)
	}
	n.requestTemplate.BaseURL = srv.URL + "/v3/mail/send"

	const (
		to      = "Juana Perez <juana.perez@example.com>"
		subject = "Decision on the case of Juana Perez"
	)
	if err := n.Send(context.Background(), &notification.Message{To: to, Subject: subject, Text: "Hola"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "email sent via SendGrid") {
		t.Fatalf("success log not emitted; got %q", out)
	}
	if !strings.Contains(out, `"status":202`) {
		t.Errorf("success log lacks the status code; got %q", out)
	}
	if !strings.Contains(out, `"message_id":"msg-123"`) {
		t.Errorf("success log lacks the SendGrid message ID; got %q", out)
	}
	for _, leaked := range []string{"juana.perez@example.com", "Juana Perez", subject, `"to"`, `"subject"`} {
		if strings.Contains(out, leaked) {
			t.Errorf("log contains %q; got %q", leaked, out)
		}
	}
}
