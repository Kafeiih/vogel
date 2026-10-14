package config

import (
	"os"
	"strings"
	"testing"

	"github.com/kafeiih/vogel/config"
)

// unsetEnv removes key for the duration of the test and restores it after.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // registers the restore
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("os.Unsetenv(%q): %v", key, err)
	}
}

func TestParseCerbosConfig(t *testing.T) {
	tests := []struct {
		name          string
		plaintext     string
		wantPlaintext bool
	}{
		{name: "unset uses TLS", plaintext: "", wantPlaintext: false},
		{name: "explicit false uses TLS", plaintext: "false", wantPlaintext: false},
		{name: "explicit true opts out of TLS", plaintext: "true", wantPlaintext: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CERBOS_HOST", "cerbos:3593")
			t.Setenv("CERBOS_PLAINTEXT", tt.plaintext)
			unsetEnv(t, "CERBOS_TLS")

			var errs config.Errors
			got := parseCerbosConfig(&errs)

			if err := errs.Err(); err != nil {
				t.Fatalf("unexpected config error: %v", err)
			}
			if got.Host != "cerbos:3593" {
				t.Errorf("Host = %q, want %q", got.Host, "cerbos:3593")
			}
			if got.Plaintext != tt.wantPlaintext {
				t.Errorf("Plaintext = %v, want %v", got.Plaintext, tt.wantPlaintext)
			}
		})
	}
}

// CERBOS_TLS was replaced by CERBOS_PLAINTEXT. A deployment that still sets
// it must fail at boot instead of being silently switched to TLS and failing
// later on every authorization call.
func TestParseCerbosConfig_LeftoverCerbosTLS_IsConfigError(t *testing.T) {
	for _, value := range []string{"true", "false", ""} {
		t.Run("CERBOS_TLS="+value, func(t *testing.T) {
			t.Setenv("CERBOS_TLS", value)

			var errs config.Errors
			parseCerbosConfig(&errs)

			err := errs.Err()
			if err == nil {
				t.Fatal("errs.Err() = nil, want an error for a leftover CERBOS_TLS")
			}
			for _, want := range []string{"CERBOS_TLS", "CERBOS_PLAINTEXT"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %s", err, want)
				}
			}
		})
	}
}
