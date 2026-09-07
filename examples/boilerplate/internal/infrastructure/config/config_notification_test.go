package config

import (
	"os"
	"testing"
)

func TestNotificationConfig_Defaults(t *testing.T) {
	os.Unsetenv("NOTIFICATION_PROVIDER")
	os.Unsetenv("NOTIFICATION_DEFAULT_FROM")
	os.Unsetenv("SMTP_HOST")
	os.Unsetenv("SENDGRID_API_KEY")

	cfg := parseNotificationConfig()

	if cfg.Provider != "smtp" {
		t.Errorf("Provider: got %q, want %q", cfg.Provider, "smtp")
	}
	if cfg.DefaultFrom != "" {
		t.Errorf("DefaultFrom: got %q, want empty", cfg.DefaultFrom)
	}
}

func TestNotificationConfig_SMTP(t *testing.T) {
	t.Setenv("NOTIFICATION_PROVIDER", "smtp")
	t.Setenv("NOTIFICATION_DEFAULT_FROM", "noreply@slep.gob.cl")
	t.Setenv("SMTP_HOST", "smtp.gmail.com")
	t.Setenv("SMTP_PORT", "587")
	t.Setenv("SMTP_USERNAME", "user@gmail.com")
	t.Setenv("SMTP_PASSWORD", "secret")

	cfg := parseNotificationConfig()

	if cfg.Provider != "smtp" {
		t.Errorf("Provider: got %q, want %q", cfg.Provider, "smtp")
	}
	if cfg.DefaultFrom != "noreply@slep.gob.cl" {
		t.Errorf("DefaultFrom: got %q, want %q", cfg.DefaultFrom, "noreply@slep.gob.cl")
	}
	if cfg.SMTP.Host != "smtp.gmail.com" {
		t.Errorf("SMTP.Host: got %q, want %q", cfg.SMTP.Host, "smtp.gmail.com")
	}
	if cfg.SMTP.Port != 587 {
		t.Errorf("SMTP.Port: got %d, want %d", cfg.SMTP.Port, 587)
	}
	if cfg.SMTP.Username != "user@gmail.com" {
		t.Errorf("SMTP.Username: got %q", cfg.SMTP.Username)
	}
	if cfg.SMTP.Password != "secret" {
		t.Errorf("SMTP.Password: got %q", cfg.SMTP.Password)
	}
}

func TestNotificationConfig_SendGrid(t *testing.T) {
	t.Setenv("NOTIFICATION_PROVIDER", "sendgrid")
	t.Setenv("NOTIFICATION_DEFAULT_FROM", "noreply@institution.cl")
	t.Setenv("SENDGRID_API_KEY", "SG.test-key-123")

	cfg := parseNotificationConfig()

	if cfg.Provider != "sendgrid" {
		t.Errorf("Provider: got %q, want %q", cfg.Provider, "sendgrid")
	}
	if cfg.SendGrid.APIKey != "SG.test-key-123" {
		t.Errorf("SendGrid.APIKey: got %q", cfg.SendGrid.APIKey)
	}
}

func TestNotificationConfig_DefaultPort(t *testing.T) {
	os.Unsetenv("SMTP_PORT")

	cfg := parseNotificationConfig()

	if cfg.SMTP.Port != 587 {
		t.Errorf("SMTP.Port default: got %d, want 587", cfg.SMTP.Port)
	}
}

func TestNotificationConfig_InvalidPort(t *testing.T) {
	t.Setenv("SMTP_PORT", "not-a-number")

	cfg := parseNotificationConfig()

	if cfg.SMTP.Port != 587 {
		t.Errorf("SMTP.Port fallback: got %d, want 587", cfg.SMTP.Port)
	}
}
