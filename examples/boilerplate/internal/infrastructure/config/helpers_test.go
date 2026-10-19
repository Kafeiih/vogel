package config

import (
	"os"
	"testing"
)

// unsetEnv removes key for the duration of the test and restores it after.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // registers the restore
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("os.Unsetenv(%q): %v", key, err)
	}
}

// setRequiredEnv sets the variables Load requires and clears the optional
// ones a developer's shell might carry into the test.
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
		"WORKER_POLL_ONLY",
	} {
		unsetEnv(t, key)
	}
}
