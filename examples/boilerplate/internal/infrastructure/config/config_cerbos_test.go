package config

import "testing"

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

			got := parseCerbosConfig()

			if got.Host != "cerbos:3593" {
				t.Errorf("Host = %q, want %q", got.Host, "cerbos:3593")
			}
			if got.Plaintext != tt.wantPlaintext {
				t.Errorf("Plaintext = %v, want %v", got.Plaintext, tt.wantPlaintext)
			}
		})
	}
}
