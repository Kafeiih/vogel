package config

import (
	"strings"
	"testing"

	"github.com/kafeiih/vogel/config"
)

// Swagger is an explicit opt-in (SWAGGER_ENABLED), independent of APP_ENV:
// the deploy compose runs with APP_ENV=development, and it must not publish
// the API description.
func TestParseSwaggerEnabled(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    bool
		wantErr bool
	}{
		{name: "unset stays off", value: "", want: false},
		{name: "explicit true turns it on", value: "true", want: true},
		{name: "explicit false stays off", value: "false", want: false},
		// A typo must not be read as "off" (or "on") silently: the operator
		// learns at boot that the value means nothing.
		{name: "unparsable value is a config error", value: "yes please", want: false, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				unsetEnv(t, "SWAGGER_ENABLED")
			} else {
				t.Setenv("SWAGGER_ENABLED", tt.value)
			}

			var errs config.Errors
			got := parseSwaggerEnabled(&errs)

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
	setRequiredEnv(t, "development")
	unsetEnv(t, "SWAGGER_ENABLED")

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

// Whether the environment is production is decided by Config.IsProduction
// alone, so the Swagger refusal cannot drift from every other
// production-only rule.
func TestLoad_SwaggerRefusedInProduction(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		value   string
		wantErr bool
	}{
		{name: "true in production is a config error", env: "production", value: "true", wantErr: true},
		{name: "unset in production is fine", env: "production", value: "", wantErr: false},
		{name: "true in staging is allowed", env: "staging", value: "true", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnv(t, tt.env)
			if tt.value == "" {
				unsetEnv(t, "SWAGGER_ENABLED")
			} else {
				t.Setenv("SWAGGER_ENABLED", tt.value)
			}

			cfg, err := Load()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "SWAGGER_ENABLED") {
					t.Fatalf("Load() error = %v, want an error naming SWAGGER_ENABLED", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.IsProduction() != (tt.env == "production") {
				t.Errorf("IsProduction() = %v with APP_ENV=%s", cfg.IsProduction(), tt.env)
			}
		})
	}
}
