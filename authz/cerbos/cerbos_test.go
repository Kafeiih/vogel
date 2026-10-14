package cerbos

import (
	"reflect"
	"testing"

	"github.com/cerbos/cerbos-sdk-go/cerbos"
)

func TestNew_EmptyHost_ReturnsError(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Error("expected error for empty host, got nil")
	}
}

func TestChecker_Close_IsANoOpThatReturnsNil(t *testing.T) {
	c, err := New(Config{Host: "localhost:3593"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// appliedPlaintext applies opts to a fresh SDK connection config and reports
// the resulting Plaintext flag. The config type lives in an internal package
// of cerbos-sdk-go, so it is built and read through reflection; nothing dials.
func appliedPlaintext(t *testing.T, opts []cerbos.Opt) bool {
	t.Helper()
	cfgType := reflect.TypeOf(cerbos.Opt(nil)).In(0).Elem()
	cfg := reflect.New(cfgType)
	for _, opt := range opts {
		reflect.ValueOf(opt).Call([]reflect.Value{cfg})
	}
	field := cfg.Elem().FieldByName("Plaintext")
	if !field.IsValid() {
		t.Fatalf("cerbos-sdk-go config %s has no Plaintext field", cfgType)
	}
	return field.Bool()
}

func TestClientOptions_TransportSecurity(t *testing.T) {
	tests := []struct {
		name          string
		cfg           Config
		wantPlaintext bool
	}{
		{name: "zero value uses TLS", cfg: Config{Host: "cerbos:3593"}, wantPlaintext: false},
		{name: "explicit plaintext opt-out", cfg: Config{Host: "cerbos:3593", Plaintext: true}, wantPlaintext: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := appliedPlaintext(t, clientOptions(tt.cfg)); got != tt.wantPlaintext {
				t.Errorf("clientOptions(%+v) sets Plaintext = %v, want %v", tt.cfg, got, tt.wantPlaintext)
			}
		})
	}
}
