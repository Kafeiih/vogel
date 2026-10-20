package config

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kafeiih/vogel/auth/zitadel"
)

func TestLoad_DBRequireTLS(t *testing.T) {
	tests := []struct {
		name    string
		appEnv  string
		value   string
		want    bool
		wantErr bool
	}{
		{name: "unset in development requires TLS", appEnv: "development", value: "", want: true},
		{name: "unset in production requires TLS", appEnv: "production", value: "", want: true},
		{name: "explicit false opts out in development", appEnv: "development", value: "false", want: false},
		{name: "explicit false opts out even in production", appEnv: "production", value: "false", want: false},
		{name: "explicit true requires TLS", appEnv: "development", value: "true", want: true},
		{name: "unparsable value is a config error", appEnv: "development", value: "nope", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnv(t, tt.appEnv)
			if tt.value != "" {
				t.Setenv("DB_REQUIRE_TLS", tt.value)
			}

			cfg, err := Load()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "DB_REQUIRE_TLS") {
					t.Fatalf("Load() error = %v, want an error naming DB_REQUIRE_TLS", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Database.RequireTLS != tt.want {
				t.Errorf("Database.RequireTLS = %v, want %v", cfg.Database.RequireTLS, tt.want)
			}
		})
	}
}

func TestLoad_ZitadelDefaults(t *testing.T) {
	setRequiredEnv(t, "production")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	z := cfg.Zitadel
	if z.Issuer != "https://auth.example.com" || z.ClientID != "client" {
		t.Errorf("Issuer, ClientID = %q, %q", z.Issuer, z.ClientID)
	}
	if z.HTTPTimeout != zitadel.DefaultHTTPTimeout {
		t.Errorf("HTTPTimeout = %v, want %v", z.HTTPTimeout, zitadel.DefaultHTTPTimeout)
	}
	if z.MaxTokenLifetime != defaultMaxTokenLifetime {
		t.Errorf("MaxTokenLifetime = %v, want %v", z.MaxTokenLifetime, defaultMaxTokenLifetime)
	}
	if len(z.AllowedAuthorizedParties) != 0 {
		t.Errorf("AllowedAuthorizedParties = %q, want none", z.AllowedAuthorizedParties)
	}
	if z.Introspection != nil {
		t.Errorf("Introspection = %+v, want nil (local JWT verification)", z.Introspection)
	}
}

func TestLoad_ZitadelSettings(t *testing.T) {
	setRequiredEnv(t, "production")
	t.Setenv("ZITADEL_HTTP_TIMEOUT", "3s")
	t.Setenv("ZITADEL_MAX_TOKEN_LIFETIME", "1h")
	t.Setenv("ZITADEL_ALLOWED_AUTHORIZED_PARTIES", " web-app , mobile-app ")
	t.Setenv("ZITADEL_INTROSPECTION", "true")
	t.Setenv("ZITADEL_INTROSPECTION_CLIENT_SECRET", "s3cret")
	t.Setenv("ZITADEL_INTROSPECTION_CACHE_TTL", "10s")
	t.Setenv("ZITADEL_INTROSPECTION_CACHE_MAX_ENTRIES", "500")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	z := cfg.Zitadel
	if z.HTTPTimeout != 3*time.Second {
		t.Errorf("HTTPTimeout = %v, want 3s", z.HTTPTimeout)
	}
	if z.MaxTokenLifetime != time.Hour {
		t.Errorf("MaxTokenLifetime = %v, want 1h", z.MaxTokenLifetime)
	}
	if want := []string{"web-app", "mobile-app"}; !slices.Equal(z.AllowedAuthorizedParties, want) {
		t.Errorf("AllowedAuthorizedParties = %q, want %q", z.AllowedAuthorizedParties, want)
	}
	want := zitadel.IntrospectionConfig{ClientSecret: "s3cret", CacheTTL: 10 * time.Second, CacheMaxEntries: 500}
	if z.Introspection == nil || *z.Introspection != want {
		t.Errorf("Introspection = %+v, want %+v", z.Introspection, want)
	}
}

func TestLoad_ZitadelKeyFileIntrospection(t *testing.T) {
	setRequiredEnv(t, "production")
	t.Setenv("ZITADEL_INTROSPECTION", "true")
	t.Setenv("ZITADEL_INTROSPECTION_KEY_FILE", "/run/secrets/zitadel-key.json")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if z := cfg.Zitadel.Introspection; z == nil || z.KeyFile != "/run/secrets/zitadel-key.json" || z.ClientSecret != "" {
		t.Errorf("Introspection = %+v, want KeyFile only", z)
	}
}

func TestLoad_ZitadelInvalidSettings(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantVar string
	}{
		{name: "negative HTTP timeout", env: map[string]string{"ZITADEL_HTTP_TIMEOUT": "-1s"}, wantVar: "ZITADEL_HTTP_TIMEOUT"},
		{name: "zero HTTP timeout", env: map[string]string{"ZITADEL_HTTP_TIMEOUT": "0s"}, wantVar: "ZITADEL_HTTP_TIMEOUT"},
		{name: "malformed max token lifetime", env: map[string]string{"ZITADEL_MAX_TOKEN_LIFETIME": "a day"}, wantVar: "ZITADEL_MAX_TOKEN_LIFETIME"},
		{name: "negative max token lifetime", env: map[string]string{"ZITADEL_MAX_TOKEN_LIFETIME": "-1h"}, wantVar: "ZITADEL_MAX_TOKEN_LIFETIME"},
		{name: "unparsable introspection flag", env: map[string]string{"ZITADEL_INTROSPECTION": "on"}, wantVar: "ZITADEL_INTROSPECTION"},
		{name: "introspection without a credential", env: map[string]string{"ZITADEL_INTROSPECTION": "true"}, wantVar: "ZITADEL_INTROSPECTION"},
		{
			name: "introspection with both credentials",
			env: map[string]string{
				"ZITADEL_INTROSPECTION":               "true",
				"ZITADEL_INTROSPECTION_KEY_FILE":      "/k.json",
				"ZITADEL_INTROSPECTION_CLIENT_SECRET": "s",
			},
			wantVar: "ZITADEL_INTROSPECTION",
		},
		{
			name:    "credential without introspection",
			env:     map[string]string{"ZITADEL_INTROSPECTION_CLIENT_SECRET": "s"},
			wantVar: "ZITADEL_INTROSPECTION",
		},
		{
			name:    "negative cache TTL",
			env:     map[string]string{"ZITADEL_INTROSPECTION": "true", "ZITADEL_INTROSPECTION_CLIENT_SECRET": "s", "ZITADEL_INTROSPECTION_CACHE_TTL": "-1s"},
			wantVar: "ZITADEL_INTROSPECTION_CACHE_TTL",
		},
		{
			name:    "negative cache size",
			env:     map[string]string{"ZITADEL_INTROSPECTION": "true", "ZITADEL_INTROSPECTION_CLIENT_SECRET": "s", "ZITADEL_INTROSPECTION_CACHE_MAX_ENTRIES": "-5"},
			wantVar: "ZITADEL_INTROSPECTION_CACHE_MAX_ENTRIES",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnv(t, "production")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tt.wantVar) {
				t.Fatalf("Load() error = %v, want an error naming %s", err, tt.wantVar)
			}
		})
	}
}

func TestMigrateRequiresTLS(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    bool
		wantErr bool
	}{
		{name: "unset defaults to true", value: "", want: true},
		{name: "explicit false", value: "false", want: false},
		{name: "explicit true", value: "true", want: true},
		{name: "unparsable is an error", value: "maybe", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DB_REQUIRE_TLS", tt.value)
			got, err := MigrateRequiresTLS()
			if (err != nil) != tt.wantErr {
				t.Fatalf("MigrateRequiresTLS() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("MigrateRequiresTLS() = %v, want %v", got, tt.want)
			}
		})
	}
}
