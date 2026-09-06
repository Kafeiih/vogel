package smtp

import (
	"log/slog"
	"testing"
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
