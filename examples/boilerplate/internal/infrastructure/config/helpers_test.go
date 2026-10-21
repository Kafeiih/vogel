package config

import (
	"os"
	"testing"
)

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("os.Unsetenv(%q): %v", key, err)
	}
}

func setRequiredEnv(t *testing.T, appEnv string) {
	t.Helper()
	t.Setenv("APP_ENV", appEnv)
	t.Setenv("DB_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("ZITADEL_ISSUER", "https://auth.example.com")
	t.Setenv("ZITADEL_CLIENT_ID", "client")
	for _, key := range []string{
		"SWAGGER_ENABLED", "CERBOS_TLS", "TRUSTED_PROXIES", "DB_REQUIRE_TLS", "DB_ACQUIRE_TIMEOUT", "DB_LEAK_THRESHOLD",
		"ZITADEL_HTTP_TIMEOUT", "ZITADEL_MAX_TOKEN_LIFETIME", "ZITADEL_ALLOWED_AUTHORIZED_PARTIES",
		"ZITADEL_INTROSPECTION", "ZITADEL_INTROSPECTION_KEY_FILE", "ZITADEL_INTROSPECTION_CLIENT_SECRET",
		"ZITADEL_INTROSPECTION_CACHE_TTL", "ZITADEL_INTROSPECTION_CACHE_MAX_ENTRIES",
		"WORKER_POLL_ONLY", "WORKER_METRICS_LISTEN_ADDR",
	} {
		unsetEnv(t, key)
	}
	// vogel:component bpms begin
	t.Setenv("GOTENBERG_URL", "http://localhost:3000")
	t.Setenv("STORAGE_CONFIGS", "documents")
	unsetEnv(t, "GOTENBERG_TIMEOUT")
	unsetEnv(t, "BPMS_DOCUMENT_STORAGE")
	// vogel:component bpms end
}
