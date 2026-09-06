package sendgrid

import (
	"log/slog"
	"testing"
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
