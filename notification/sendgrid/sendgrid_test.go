package sendgrid

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kafeiih/vogel/notification"
)

func TestNewSendGridNotifier_ValidConfig(t *testing.T) {
	cfg := Config{
		APIKey: "SG.test-key-123",
	}

	n, err := NewSendGridNotifier(cfg, "noreply@inst.cl", slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n == nil {
		t.Fatal("expected non-nil notifier")
	}
}

func TestNewSendGridNotifier_EmptyAPIKey(t *testing.T) {
	cfg := Config{
		APIKey: "",
	}

	_, err := NewSendGridNotifier(cfg, "noreply@inst.cl", slog.Default())
	if err == nil {
		t.Fatal("expected error for empty API key")
	}
}

func TestNewSendGridNotifier_NilLogger(t *testing.T) {
	cfg := Config{
		APIKey: "SG.test",
	}

	_, err := NewSendGridNotifier(cfg, "noreply@inst.cl", nil)
	if err == nil {
		t.Fatal("expected error for nil logger")
	}
}

func TestSGEmail_SplitsDisplayName(t *testing.T) {
	e, err := sgEmail("Ana Pérez <ana@example.com>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Name != "Ana Pérez" || e.Address != "ana@example.com" {
		t.Errorf("got Name=%q Address=%q", e.Name, e.Address)
	}
}

func TestSGEmail_RejectsMalformed(t *testing.T) {
	if _, err := sgEmail("not an address"); err == nil {
		t.Error("expected error")
	}
}

func TestNewSendGridNotifier_DefaultFrom(t *testing.T) {
	cfg := Config{APIKey: "SG.test"}
	cases := []struct {
		name        string
		defaultFrom string
		wantErr     bool
	}{
		{"empty is allowed", "", false},
		{"bare address", "noreply@inst.cl", false},
		{"display name", `"Institución" <noreply@inst.cl>`, false},
		{"missing domain", "noreply@", true},
		{"header injection", "noreply@inst.cl\r\nBcc: victim@example.com", true},
		{"two addresses", "a@inst.cl, b@inst.cl", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSendGridNotifier(cfg, tc.defaultFrom, slog.Default())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), tc.defaultFrom) {
				t.Errorf("error %q echoes the address", err)
			}
		})
	}
}

func TestResolveFrom(t *testing.T) {
	cases := []struct {
		name, msgFrom, defaultFrom string
		want                       string
		wantErr                    bool
	}{
		{"message From wins", "ana@inst.cl", "noreply@inst.cl", "ana@inst.cl", false},
		{"default used when From is empty", "", "noreply@inst.cl", "noreply@inst.cl", false},
		{"both empty", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveFrom(tc.msgFrom, tc.defaultFrom)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSend_NoFromAndNoDefault(t *testing.T) {
	// Fails before any HTTP call: the message passes Validate (From is
	// optional) but there is no sender to fall back to.
	n, err := NewSendGridNotifier(Config{APIKey: "SG.test"}, "", slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = n.Send(context.Background(), &notification.Message{
		To: "user@example.com", Subject: "Welcome", Text: "Hello",
	})
	if err == nil || !strings.Contains(err.Error(), "no default") {
		t.Fatalf("err = %v, want missing From / no default error", err)
	}
}

// TestSend_SplitsNameAndAddress drives Send end to end against a local
// httptest server (sendgrid.Client embeds rest.Request, so its BaseURL can be
// redirected in-package without network access) and asserts the JSON body
// carries From and To as separate name/email fields, not the whole
// "Name <addr>" string as the address.
func TestSend_SplitsNameAndAddress(t *testing.T) {
	type email struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	var body struct {
		From             email `json:"from"`
		Personalizations []struct {
			To []email `json:"to"`
		} `json:"personalizations"`
	}
	var decodeErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decodeErr = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	n, err := NewSendGridNotifier(Config{APIKey: "SG.test"}, "Default Sender <noreply@inst.cl>", slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	n.requestTemplate.BaseURL = srv.URL + "/v3/mail/send"

	tests := []struct {
		name     string
		from     string
		wantFrom email
	}{
		{name: "message From with display name", from: "Soporte <soporte@inst.cl>", wantFrom: email{Name: "Soporte", Email: "soporte@inst.cl"}},
		{name: "default From with display name", from: "", wantFrom: email{Name: "Default Sender", Email: "noreply@inst.cl"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := n.Send(context.Background(), &notification.Message{
				From: tt.from, To: "Juana Pérez <juana@example.com>", Subject: "Hola", Text: "Hola",
			})
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if decodeErr != nil {
				t.Fatalf("decoding request body: %v", decodeErr)
			}
			if body.From != tt.wantFrom {
				t.Errorf("from = %+v, want %+v", body.From, tt.wantFrom)
			}
			if len(body.Personalizations) != 1 || len(body.Personalizations[0].To) != 1 {
				t.Fatalf("personalizations = %+v, want exactly one recipient", body.Personalizations)
			}
			wantTo := email{Name: "Juana Pérez", Email: "juana@example.com"}
			if got := body.Personalizations[0].To[0]; got != wantTo {
				t.Errorf("to = %+v, want %+v", got, wantTo)
			}
		})
	}
}
