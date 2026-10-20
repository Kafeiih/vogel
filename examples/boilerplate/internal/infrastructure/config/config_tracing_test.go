package config

import (
	"strings"
	"testing"

	"github.com/kafeiih/vogel/config"
	"github.com/kafeiih/vogel/otel"
)

func parseTracing(t *testing.T) (otel.Config, error) {
	t.Helper()
	var errs config.Errors
	cfg := parseTracingConfig(&errs, "production", "1.2.3")
	return cfg, errs.Err()
}

func TestParseTracingConfig_OffWithoutEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "")

	cfg, err := parseTracing(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Endpoint != "" || cfg.SampleRatio != 0 {
		t.Errorf("got %+v, want tracing off with the default ratio", cfg)
	}
}

func TestParseTracingConfig_ReadsTheStandardNames(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4318")
	t.Setenv("OTEL_SERVICE_NAME", "billing")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.25")

	cfg, err := parseTracing(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := otel.Config{
		Endpoint:       "http://otel-collector:4318",
		ServiceName:    "billing",
		ServiceVersion: "1.2.3",
		Environment:    "production",
		SampleRatio:    0.25,
	}
	if cfg.Endpoint != want.Endpoint || cfg.ServiceName != want.ServiceName || cfg.ServiceVersion != want.ServiceVersion ||
		cfg.Environment != want.Environment || cfg.SampleRatio != want.SampleRatio {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestParseTracingConfig_InvalidSampleRatio(t *testing.T) {
	for _, v := range []string{"abc", "-0.1", "0", "1.5", "NaN"} {
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4318")
		t.Setenv("OTEL_TRACES_SAMPLER_ARG", v)

		_, err := parseTracing(t)
		if err == nil || !strings.Contains(err.Error(), "OTEL_TRACES_SAMPLER_ARG") {
			t.Errorf("OTEL_TRACES_SAMPLER_ARG=%q: err = %v, want one naming the variable", v, err)
		}
	}
}

func TestParseTracingConfig_InvalidEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector:4318")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "")

	_, err := parseTracing(t)
	if err == nil || !strings.Contains(err.Error(), "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Errorf("err = %v, want one naming OTEL_EXPORTER_OTLP_ENDPOINT", err)
	}
}

func TestLoad_TracingTakesTheAppEnvironmentAndVersion(t *testing.T) {
	setRequiredEnv(t, "staging")
	t.Setenv("APP_VERSION", "2.4.0")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.App.Env != "staging" || cfg.Tracing.Environment != "staging" {
		t.Errorf("App.Env = %q, Tracing.Environment = %q, want both %q", cfg.App.Env, cfg.Tracing.Environment, "staging")
	}
	if cfg.App.Version != "2.4.0" || cfg.Tracing.ServiceVersion != "2.4.0" {
		t.Errorf("App.Version = %q, Tracing.ServiceVersion = %q, want both %q", cfg.App.Version, cfg.Tracing.ServiceVersion, "2.4.0")
	}
}
