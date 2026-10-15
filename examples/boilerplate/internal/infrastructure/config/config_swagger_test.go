package config

import (
	"strings"
	"testing"

	"github.com/kafeiih/vogel/config"
)

// Swagger is an explicit opt-in (SWAGGER_ENABLED), independent of APP_ENV:
// the deploy compose runs with APP_ENV=development because its bundled
// Postgres serves no TLS, and it must not publish the API description.
func TestParseSwaggerEnabled(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		value   string
		want    bool
		wantErr bool
	}{
		{name: "unset in development stays off", env: "development", value: "", want: false},
		{name: "explicit true in development turns it on", env: "development", value: "true", want: true},
		{name: "explicit false stays off", env: "staging", value: "false", want: false},
		{name: "unparsable value fails closed", env: "development", value: "yes please", want: false},
		{name: "explicit true in staging turns it on", env: "staging", value: "true", want: true},
		{name: "unset in production stays off", env: "production", value: "", want: false},
		{name: "true in production is a config error", env: "production", value: "true", want: false, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				unsetEnv(t, "SWAGGER_ENABLED")
			} else {
				t.Setenv("SWAGGER_ENABLED", tt.value)
			}

			var errs config.Errors
			got := parseSwaggerEnabled(&errs, tt.env)

			err := errs.Err()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "SWAGGER_ENABLED") {
					t.Fatalf("errs.Err() = %v, want an error naming SWAGGER_ENABLED", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected config error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseSwaggerEnabled = %v, want %v", got, tt.want)
			}
		})
	}
}

// Load carries the flag into AppConfig, so main.go never derives it from
// APP_ENV again.
func TestLoad_SwaggerDefaultsToDisabled(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DB_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("ZITADEL_ISSUER", "https://auth.example.com")
	t.Setenv("ZITADEL_CLIENT_ID", "client")
	unsetEnv(t, "SWAGGER_ENABLED")
	unsetEnv(t, "CERBOS_TLS")
	unsetEnv(t, "TRUSTED_PROXIES")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.App.SwaggerEnabled {
		t.Errorf("App.SwaggerEnabled = true with SWAGGER_ENABLED unset, want false")
	}

	t.Setenv("SWAGGER_ENABLED", "true")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.App.SwaggerEnabled {
		t.Errorf("App.SwaggerEnabled = false with SWAGGER_ENABLED=true, want true")
	}
}
