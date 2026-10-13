package smtp

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/kafeiih/vogel/notification"
)

func TestNewSMTPNotifier_ValidConfig(t *testing.T) {
	cfg := Config{
		Host:     "smtp.gmail.com",
		Port:     587,
		Username: "user@gmail.com",
		Password: "secret",
	}

	n, err := NewSMTPNotifier(cfg, "noreply@inst.cl", slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n == nil {
		t.Fatal("expected non-nil notifier")
	}
}

func TestNewSMTPNotifier_EmptyHost(t *testing.T) {
	cfg := Config{
		Host: "",
		Port: 587,
	}

	_, err := NewSMTPNotifier(cfg, "noreply@inst.cl", slog.Default())
	if err == nil {
		t.Fatal("expected error for empty host")
	}
}

func TestNewSMTPNotifier_NilLogger(t *testing.T) {
	cfg := Config{
		Host: "smtp.test.com",
		Port: 587,
	}

	_, err := NewSMTPNotifier(cfg, "noreply@inst.cl", nil)
	if err == nil {
		t.Fatal("expected error for nil logger")
	}
}

func TestNewSMTPNotifier_DefaultFrom(t *testing.T) {
	cfg := Config{Host: "smtp.test.com", Port: 587}
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
			_, err := NewSMTPNotifier(cfg, tc.defaultFrom, slog.Default())
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
	// Fails before any network I/O: the message passes Validate (From is
	// optional) but there is no sender to fall back to.
	n, err := NewSMTPNotifier(Config{Host: "smtp.invalid", Port: 587}, "", slog.Default())
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
