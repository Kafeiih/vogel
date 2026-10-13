package config

import (
	"strings"
	"testing"

	"github.com/kafeiih/vogel/config"
)

func TestTrustedProxies(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    []string
		wantErr bool
	}{
		{name: "unset trusts no proxy", value: "", want: nil},
		{name: "CIDRs and single IPs", value: "10.0.0.0/8, 192.168.1.1", want: []string{"10.0.0.0/8", "192.168.1.1/32"}},
		{name: "invalid entry is a config error", value: "10.0.0.0/8,nope", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TRUSTED_PROXIES", tt.value)

			var errs config.Errors
			got := parseTrustedProxies(&errs)

			if tt.wantErr {
				err := errs.Err()
				if err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXIES") {
					t.Fatalf("errs.Err() = %v, want an error naming TRUSTED_PROXIES", err)
				}
				if got != nil {
					t.Errorf("got %v on error, want nil", got)
				}
				return
			}
			if err := errs.Err(); err != nil {
				t.Fatalf("unexpected config error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i, p := range got {
				if p.String() != tt.want[i] {
					t.Errorf("prefix[%d] = %s, want %s", i, p, tt.want[i])
				}
			}
		})
	}
}
