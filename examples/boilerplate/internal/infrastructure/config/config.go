// Package config loads this application's configuration from environment
// variables.
//
// Every primitive read (String, Bool, Int32, Duration, Require, ...) comes
// from vogel/config -- this package only shapes the struct and picks the env
// var names, exactly as vogel/config's own package doc comment says a
// consumer should. vogel deliberately does NOT define application config
// structs (Config, AppConfig, DatabaseConfig, ...) itself, so those stay
// here.
//
// Wherever an adapter's own Config type already matches this application's
// needs field-for-field (postgres.Config, zitadel.Config, cerbos.Config and
// the other adapters' Config types), this package embeds that type directly instead of mirroring it with a redundant local
// struct that would need converting at the call site in main.go. Only the
// fields with no vogel equivalent (App, CORS, Metrics) get a local type.
package config

import (
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	// vogel:component worker begin
	"github.com/kafeiih/vogel/worker"

	// vogel:component worker end
	// vogel:component notification begin
	"github.com/kafeiih/vogel/notification/sendgrid"
	"github.com/kafeiih/vogel/notification/smtp"

	// vogel:component notification end
	// vogel:component storage begin
	"github.com/kafeiih/vogel/storage/s3"

	// vogel:component storage end
	"github.com/kafeiih/vogel/auth/zitadel"
	"github.com/kafeiih/vogel/authz/cerbos"
	"github.com/kafeiih/vogel/config"
	vmw "github.com/kafeiih/vogel/httpx/middleware"
	"github.com/kafeiih/vogel/postgres"
)

// Config holds every setting this application needs to boot.
type Config struct {
	App      AppConfig
	Database postgres.Config
	Zitadel  zitadel.Config
	Cerbos   cerbos.Config
	CORS     CORSConfig
	Metrics  MetricsConfig
	// vogel:component worker begin

	Worker worker.Config
	// vogel:component worker end
	// vogel:component storage begin

	Storages map[string]s3.Config
	// vogel:component storage end
	// vogel:component notification begin

	Notification NotificationConfig
	// vogel:component notification end
}

// AppConfig holds settings with no vogel equivalent: this application's own
// identity, HTTP timeout, and the reverse proxies allowed to report the
// client IP.
type AppConfig struct {
	Env     string // development, staging, production
	Port    string // :8080
	Url     string // localhost:8080
	Version string
	Timeout time.Duration // 30s

	// TrustedProxies (TRUSTED_PROXIES, comma-separated CIDRs or IPs, default
	// empty) are the only peers whose X-Forwarded-For / X-Real-IP headers
	// vmw.RealIP believes. Empty means no header is trusted and the client
	// IP is always the TCP peer.
	TrustedProxies []netip.Prefix

	// SwaggerEnabled (SWAGGER_ENABLED, default false) serves the Swagger UI
	// and spec under /v1/swagger/. It is an explicit opt-in rather than
	// "APP_ENV is not production": the deploy compose runs with
	// APP_ENV=development and must not publish the API description. Load
	// refuses it when IsProduction. See parseSwaggerEnabled.
	SwaggerEnabled bool
}

// MetricsConfig holds configuration for the dedicated Prometheus metrics
// server. vogel does not run one of its own -- see metrics_server.go.
type MetricsConfig struct {
	ListenAddr string // METRICS_LISTEN_ADDR, default ":9090"
}

// vogel:component notification begin
// NotificationConfig holds configuration for email delivery. Provider picks
// which of the two vogel-shaped configs below main.go actually builds an
// adapter from.
type NotificationConfig struct {
	Provider    string          // "smtp" (default) or "sendgrid"
	DefaultFrom string          // Default sender address, e.g. "noreply@institucion.cl"
	SMTP        smtp.Config     // SMTP-specific settings
	SendGrid    sendgrid.Config // SendGrid-specific settings
}

// vogel:component notification end
// CORSConfig holds settings with no vogel equivalent -- CORS is wired
// directly with github.com/rs/cors in router.go.
type CORSConfig struct {
	AllowedOrigins []string // ["https://app.institucion.gob"]
	AllowedMethods []string // ["GET", "POST", "PUT", "DELETE"]
	AllowedHeaders []string // ["Authorization", "Content-Type"]
}

// Load reads configuration from environment variables.
//
// In non-production environments it also loads a .env file if present. It
// accumulates every validation problem via config.Errors instead of
// returning on the first one encountered, so a single boot failure reports
// every missing or invalid variable at once -- see vogel/config's package
// doc comment for the rationale.
func Load() (*Config, error) {
	// Load .env only in dev/test -- in production env vars come from the
	// platform.
	if os.Getenv("APP_ENV") != "production" {
		_ = godotenv.Load() // silently ignore if .env doesn't exist
	}

	var errs config.Errors

	dbURL := errs.Require("DB_URL")
	// TLS to the database is required unless explicitly turned off: it is
	// not derived from APP_ENV, so a deployment that forgets APP_ENV (or
	// runs with APP_ENV=development) still rejects a DSN that is not
	// sslmode=verify-ca or verify-full.
	dbRequireTLS := parseBool(&errs, "DB_REQUIRE_TLS", true)

	maxConns := errs.Int32("DB_MAX_CONNS", 25)
	minConns := errs.Int32("DB_MIN_CONNS", 5)

	appTimeout := errs.Duration("APP_TIMEOUT", 30*time.Second)
	maxConnLifetime := errs.Duration("DB_MAX_CONN_LIFETIME", time.Hour)
	maxConnIdleTime := errs.Duration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute)
	connectTimeout := errs.Duration("DB_CONNECT_TIMEOUT", 10*time.Second)

	trustedProxies := parseTrustedProxies(&errs)
	cerbosCfg := parseCerbosConfig(&errs)
	zitadelCfg := parseZitadelConfig(&errs)
	swaggerEnabled := parseSwaggerEnabled(&errs)

	cfg := &Config{
		App: AppConfig{
			Env:     config.String("APP_ENV", "development"),
			Port:    config.String("APP_PORT", ":8080"),
			Url:     config.String("APP_URL", "localhost:8080"),
			Version: config.String("APP_VERSION", "0.1.0"),
			Timeout: appTimeout,

			TrustedProxies: trustedProxies,
			SwaggerEnabled: swaggerEnabled,
		},
		Database: postgres.Config{
			URL:             dbURL,
			MaxConns:        maxConns,
			MinConns:        minConns,
			MaxConnLifetime: maxConnLifetime,
			MaxConnIdleTime: maxConnIdleTime,
			ConnectTimeout:  connectTimeout,
			RequireTLS:      dbRequireTLS,
		},
		Zitadel: zitadelCfg,
		Cerbos:  cerbosCfg,
		CORS: CORSConfig{
			AllowedOrigins: parseSlice("CORS_ALLOWED_ORIGINS", ","),
			AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"},
			AllowedHeaders: []string{"Authorization", "Content-Type", "X-Request-ID"},
		},
		Metrics: parseMetricsConfig(),
		// vogel:component worker begin

		Worker: parseWorkerConfig(),
		// vogel:component worker end
		// vogel:component storage begin

		Storages: parseStorageConfigs(&errs),
		// vogel:component storage end
		// vogel:component notification begin

		Notification: parseNotificationConfig(),
		// vogel:component notification end
	}

	cfg.validate(&errs)
	if err := errs.Err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate records the rules that depend on more than one setting.
// Production is decided by IsProduction alone, so these rules cannot drift
// from it.
func (c *Config) validate(errs *config.Errors) {
	// The API description maps every route for an unauthenticated caller;
	// an operator who set SWAGGER_ENABLED=true in production learns at boot
	// that it is refused there instead of having it silently ignored.
	if c.App.SwaggerEnabled && c.IsProduction() {
		errs.Addf("SWAGGER_ENABLED: must not be true with APP_ENV=production")
	}
}

// IsProduction reports whether we are running in production.
func (c *Config) IsProduction() bool {
	return c.App.Env == "production"
}

// parseSlice is a small helper vogel/config does not provide: a
// comma-separated list where an unset variable means "no values" rather than
// vogel/config.StringSlice's "one empty-string value".
func parseSlice(key, sep string) []string {
	value := os.Getenv(key)
	if value == "" {
		return []string{}
	}
	var result []string
	for v := range strings.SplitSeq(value, sep) {
		trimmed := strings.TrimSpace(v)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// parseTrustedProxies reads TRUSTED_PROXIES via vmw.ParseTrustedProxies and
// records an error naming the variable if any entry is invalid. Unlike most
// optional variables here, a bad value fails the boot instead of falling back
// to a default: silently trusting no proxy would make every client appear as
// the proxy's IP, and there is no safe guess for what was meant.
func parseTrustedProxies(errs *config.Errors) []netip.Prefix {
	prefixes, err := vmw.ParseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		errs.Addf("TRUSTED_PROXIES: %v", err)
		return nil
	}
	return prefixes
}

// parseSwaggerEnabled reads SWAGGER_ENABLED (default false). A value
// strconv.ParseBool cannot read is a config error, not "off": the operator
// meant something, and guessing which way is how a typo ends up publishing
// the API description. Load refuses true in production (see validate).
func parseSwaggerEnabled(errs *config.Errors) bool {
	return parseBool(errs, "SWAGGER_ENABLED", false)
}

// parseBool reads key as a strconv.ParseBool boolean, returning def when it
// is unset or empty. Unlike vogel/config.Bool, an unparsable value is
// recorded in errs instead of falling back to def: every flag read this way
// switches a security control, and a typo must fail the boot rather than
// flip it to whichever value the default happens to be.
func parseBool(errs *config.Errors, key string, def bool) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		errs.Addf("%s: must be true or false, got %q", key, raw)
		return def
	}
	return v
}

// defaultMaxTokenLifetime is ZITADEL_MAX_TOKEN_LIFETIME's default: Zitadel's
// own default access-token lifetime, so tokens minted with the issuer's
// defaults pass and anything configured to live longer is rejected.
const defaultMaxTokenLifetime = 12 * time.Hour

// parseZitadelConfig reads the Zitadel token validation settings:
//
//	ZITADEL_ISSUER, ZITADEL_CLIENT_ID       required
//	ZITADEL_HTTP_TIMEOUT                    per-request timeout towards the issuer (default 10s, > 0)
//	ZITADEL_MAX_TOKEN_LIFETIME              longest accepted exp - iat (default 12h; 0 disables the check)
//	ZITADEL_ALLOWED_AUTHORIZED_PARTIES      comma-separated client IDs accepted as azp (default: any)
//	ZITADEL_INTROSPECTION                   true validates every token with the introspection endpoint (default false)
//	ZITADEL_INTROSPECTION_KEY_FILE          API application key.json, or
//	ZITADEL_INTROSPECTION_CLIENT_SECRET     its client secret (exactly one, only with introspection)
//	ZITADEL_INTROSPECTION_CACHE_TTL         how long an active result is reused (default 30s)
//	ZITADEL_INTROSPECTION_CACHE_MAX_ENTRIES cached results (default 10000)
//
// It checks here what zitadel.New would only reject after the process
// started connecting, so every invalid setting is reported at once and
// names its variable. See zitadel.Config for what each knob protects.
func parseZitadelConfig(errs *config.Errors) zitadel.Config {
	cfg := zitadel.Config{
		Issuer:   errs.Require("ZITADEL_ISSUER"),
		ClientID: errs.Require("ZITADEL_CLIENT_ID"),

		HTTPTimeout:      errs.Duration("ZITADEL_HTTP_TIMEOUT", zitadel.DefaultHTTPTimeout),
		MaxTokenLifetime: errs.Duration("ZITADEL_MAX_TOKEN_LIFETIME", defaultMaxTokenLifetime),

		AllowedAuthorizedParties: parseSlice("ZITADEL_ALLOWED_AUTHORIZED_PARTIES", ","),
	}
	if cfg.HTTPTimeout <= 0 {
		errs.Addf("ZITADEL_HTTP_TIMEOUT: must be greater than zero")
		cfg.HTTPTimeout = zitadel.DefaultHTTPTimeout
	}
	if cfg.MaxTokenLifetime < 0 {
		errs.Addf("ZITADEL_MAX_TOKEN_LIFETIME: must not be negative")
		cfg.MaxTokenLifetime = defaultMaxTokenLifetime
	}

	introspection := parseBool(errs, "ZITADEL_INTROSPECTION", false)
	ic := zitadel.IntrospectionConfig{
		KeyFile:         os.Getenv("ZITADEL_INTROSPECTION_KEY_FILE"),
		ClientSecret:    os.Getenv("ZITADEL_INTROSPECTION_CLIENT_SECRET"),
		CacheTTL:        errs.Duration("ZITADEL_INTROSPECTION_CACHE_TTL", 0),
		CacheMaxEntries: errs.Int("ZITADEL_INTROSPECTION_CACHE_MAX_ENTRIES", 0),
	}
	hasCredential := ic.KeyFile != "" || ic.ClientSecret != ""
	switch {
	case !introspection:
		// A credential without the switch most likely means the operator
		// believes revoked tokens stop working, while they are still
		// verified locally until exp.
		if hasCredential {
			errs.Addf("ZITADEL_INTROSPECTION: a ZITADEL_INTROSPECTION_* credential is set but introspection is off; set ZITADEL_INTROSPECTION=true or remove it")
		}
		return cfg
	case (ic.KeyFile == "") == (ic.ClientSecret == ""):
		errs.Addf("ZITADEL_INTROSPECTION: needs exactly one of ZITADEL_INTROSPECTION_KEY_FILE or ZITADEL_INTROSPECTION_CLIENT_SECRET")
	}
	if ic.CacheTTL < 0 {
		errs.Addf("ZITADEL_INTROSPECTION_CACHE_TTL: must not be negative")
	}
	if ic.CacheMaxEntries < 0 {
		errs.Addf("ZITADEL_INTROSPECTION_CACHE_MAX_ENTRIES: must not be negative")
	}
	cfg.Introspection = &ic
	return cfg
}

// vogel:component worker begin
// WorkerSchema returns the PostgreSQL schema River's tables live in
// (WORKER_SCHEMA, default "river"). It reads only that variable, so "api
// migrate up" can apply River's migrations without the variables Load
// requires for serving; the worker reads the same value through
// parseWorkerConfig.
func WorkerSchema() string {
	return config.String("WORKER_SCHEMA", "river")
}

// vogel:component worker end
// MigrateRequiresTLS reads DB_REQUIRE_TLS (default true) for "api migrate",
// which connects with DB_URL directly instead of going through Load. The
// migrator role owns the schema, so it gets the same TLS policy as the
// serving process. An unparsable value is an error, as it is for Load.
func MigrateRequiresTLS() (bool, error) {
	var errs config.Errors
	v := parseBool(&errs, "DB_REQUIRE_TLS", true)
	return v, errs.Err()
}

// vogel:component worker begin
// parseWorkerConfig reads WORKER_* env vars and returns a worker.Config with
// defaults.
func parseWorkerConfig() worker.Config {
	schema := WorkerSchema()

	defaultMax := 100
	if v := os.Getenv("WORKER_DEFAULT_MAX_WORKERS"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err == nil && parsed > 0 {
			defaultMax = parsed
		}
	}

	queues := make(map[string]int)
	if raw := os.Getenv("WORKER_QUEUES"); raw != "" {
		for entry := range strings.SplitSeq(raw, ",") {
			entry = strings.TrimSpace(entry)
			parts := strings.SplitN(entry, ":", 2)
			if len(parts) != 2 {
				continue
			}
			name := strings.TrimSpace(parts[0])
			maxW, err := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err != nil || maxW <= 0 || name == "" {
				continue
			}
			queues[name] = maxW
		}
	}

	return worker.Config{
		Schema:            schema,
		DefaultMaxWorkers: defaultMax,
		Queues:            queues,
	}
}

// vogel:component worker end
// vogel:component storage begin

// Bounds for STORAGE_<NAME>_PRESIGN_EXPIRY. A presigned URL is a bearer
// credential for one object until it expires, and nothing can revoke it
// earlier, so its lifetime is capped rather than left to whatever is set.
const (
	defaultPresignExpiry = 15 * time.Minute
	maxPresignExpiry     = time.Hour
)

// parseStorageConfigs reads named storage configurations from environment
// variables.
//
// Format:
//
//	STORAGE_CONFIGS=assets,documents
//	STORAGE_ASSETS_BUCKET=app-assets
//	STORAGE_ASSETS_REGION=us-east-1
//	STORAGE_DOCUMENTS_BUCKET=documentos
//	STORAGE_DOCUMENTS_ENDPOINT=http://minio:9000
//	STORAGE_DOCUMENTS_PATH_STYLE=true
//	STORAGE_DOCUMENTS_PRESIGN_EXPIRY=5m   (default 15m, at most 1h)
//
// Every bucket stays private (Block Public Access on): clients reach objects
// only through presigned URLs, never through a public bucket policy or ACL.
//
// Returns an empty map if STORAGE_CONFIGS is not set.
func parseStorageConfigs(errs *config.Errors) map[string]s3.Config {
	raw := os.Getenv("STORAGE_CONFIGS")
	if raw == "" {
		return map[string]s3.Config{}
	}

	configs := make(map[string]s3.Config)
	for name := range strings.SplitSeq(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		prefix := "STORAGE_" + strings.ToUpper(name) + "_"

		expiryKey := prefix + "PRESIGN_EXPIRY"
		presignExpiry := errs.Duration(expiryKey, defaultPresignExpiry)
		switch {
		case presignExpiry <= 0:
			errs.Addf("%s: must be greater than zero", expiryKey)
			presignExpiry = defaultPresignExpiry
		case presignExpiry > maxPresignExpiry:
			errs.Addf("%s: must be at most %s, got %s", expiryKey, maxPresignExpiry, presignExpiry)
			presignExpiry = defaultPresignExpiry
		}

		configs[name] = s3.Config{
			Bucket:        config.String(prefix+"BUCKET", ""),
			Region:        config.String(prefix+"REGION", "us-east-1"),
			Endpoint:      config.String(prefix+"ENDPOINT", ""),
			AccessKey:     config.String(prefix+"ACCESS_KEY", ""),
			SecretKey:     config.String(prefix+"SECRET_KEY", ""),
			PathStyle:     config.Bool(prefix+"PATH_STYLE", false),
			PresignExpiry: presignExpiry,
		}
	}

	return configs
}

// vogel:component storage end
// parseCerbosConfig reads the Cerbos PDP connection. TLS is the default;
// CERBOS_PLAINTEXT=true opts out, for the local development container only;
// like every flag read with parseBool, a value it cannot read is an error.
//
// CERBOS_TLS, which CERBOS_PLAINTEXT replaced, is rejected whenever it is set
// (whatever its value) and recorded in errs: a deployment that still carries
// it would otherwise be switched to TLS silently and only fail later, with a
// 503 on every authorization call.
func parseCerbosConfig(errs *config.Errors) cerbos.Config {
	if _, ok := os.LookupEnv("CERBOS_TLS"); ok {
		errs.Addf("CERBOS_TLS is no longer read: remove it and set CERBOS_PLAINTEXT=true only if the Cerbos PDP listens without TLS (TLS is the default)")
	}
	return cerbos.Config{
		Host:      config.String("CERBOS_HOST", "localhost:3593"),
		Plaintext: parseBool(errs, "CERBOS_PLAINTEXT", false),
	}
}

// vogel:component notification begin
// parseNotificationConfig reads NOTIFICATION_*, SMTP_*, and SENDGRID_* env
// vars.
func parseNotificationConfig() NotificationConfig {
	smtpPort := 587
	if v := os.Getenv("SMTP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			smtpPort = p
		}
	}

	return NotificationConfig{
		Provider:    config.String("NOTIFICATION_PROVIDER", "smtp"),
		DefaultFrom: config.String("NOTIFICATION_DEFAULT_FROM", ""),
		SMTP: smtp.Config{
			Host:     config.String("SMTP_HOST", ""),
			Port:     smtpPort,
			Username: config.String("SMTP_USERNAME", ""),
			Password: config.String("SMTP_PASSWORD", ""),
		},
		SendGrid: sendgrid.Config{
			APIKey: config.String("SENDGRID_API_KEY", ""),
		},
	}
}

// vogel:component notification end
// parseMetricsConfig reads METRICS_* env vars and returns a MetricsConfig
// with defaults.
func parseMetricsConfig() MetricsConfig {
	return MetricsConfig{
		ListenAddr: config.String("METRICS_LISTEN_ADDR", ":9090"),
	}
}
