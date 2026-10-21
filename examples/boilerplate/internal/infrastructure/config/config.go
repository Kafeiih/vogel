package config

import (
	"net/netip"
	// vogel:component tracing begin
	"net/url"
	// vogel:component tracing end
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
	// vogel:component bpms begin
	"github.com/kafeiih/vogel/document/gotenberg"

	// vogel:component bpms end
	// vogel:component tracing begin
	"github.com/kafeiih/vogel/otel"

	// vogel:component tracing end
	"github.com/kafeiih/vogel/auth/zitadel"
	"github.com/kafeiih/vogel/authz/cerbos"
	"github.com/kafeiih/vogel/config"
	vmw "github.com/kafeiih/vogel/httpx/middleware"
	"github.com/kafeiih/vogel/postgres"
)

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
	// vogel:component tracing begin

	Tracing otel.Config
	// vogel:component tracing end
	// vogel:component bpms begin

	BPMS BPMSConfig
	// vogel:component bpms end
}

type AppConfig struct {
	Env     string
	Port    string
	Url     string
	Version string
	Timeout time.Duration

	TrustedProxies []netip.Prefix

	SwaggerEnabled bool
}

type MetricsConfig struct {
	ListenAddr string
	// vogel:component worker begin

	WorkerListenAddr string
	// vogel:component worker end
}

// vogel:component notification begin
type NotificationConfig struct {
	Provider    string
	DefaultFrom string
	SMTP        smtp.Config
	SendGrid    sendgrid.Config
}

// vogel:component notification end
// vogel:component bpms begin
type BPMSConfig struct {
	DocumentStorage string
	Gotenberg       gotenberg.Config
}

// vogel:component bpms end
type CORSConfig struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
}

func Load() (*Config, error) {
	if os.Getenv("APP_ENV") != "production" {
		_ = godotenv.Load()
	}

	var errs config.Errors

	dbURL := errs.Require("DB_URL")
	dbRequireTLS := parseBool(&errs, "DB_REQUIRE_TLS", true)

	maxConns := errs.Int32("DB_MAX_CONNS", 25)
	minConns := errs.Int32("DB_MIN_CONNS", 5)

	appTimeout := errs.Duration("APP_TIMEOUT", 30*time.Second)
	maxConnLifetime := errs.Duration("DB_MAX_CONN_LIFETIME", time.Hour)
	maxConnIdleTime := errs.Duration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute)
	connectTimeout := errs.Duration("DB_CONNECT_TIMEOUT", 10*time.Second)
	acquireTimeout := positiveDuration(&errs, "DB_ACQUIRE_TIMEOUT", defaultAcquireTimeout)
	leakThreshold := positiveDuration(&errs, "DB_LEAK_THRESHOLD", defaultLeakThreshold)

	trustedProxies := parseTrustedProxies(&errs)
	cerbosCfg := parseCerbosConfig(&errs)
	zitadelCfg := parseZitadelConfig(&errs)
	swaggerEnabled := parseSwaggerEnabled(&errs)
	appEnv := config.String("APP_ENV", "development")
	appVersion := config.String("APP_VERSION", "0.1.0")

	cfg := &Config{
		App: AppConfig{
			Env:     appEnv,
			Port:    config.String("APP_PORT", ":8080"),
			Url:     config.String("APP_URL", "localhost:8080"),
			Version: appVersion,
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
			AcquireTimeout:  acquireTimeout,
			LeakThreshold:   leakThreshold,
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

		Worker: parseWorkerConfig(&errs),
		// vogel:component worker end
		// vogel:component storage begin

		Storages: parseStorageConfigs(&errs),
		// vogel:component storage end
		// vogel:component notification begin

		Notification: parseNotificationConfig(),
		// vogel:component notification end
		// vogel:component tracing begin

		Tracing: parseTracingConfig(&errs, appEnv, appVersion),
		// vogel:component tracing end
		// vogel:component bpms begin

		BPMS: parseBPMSConfig(&errs),
		// vogel:component bpms end
	}

	cfg.validate(&errs)
	if err := errs.Err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate(errs *config.Errors) {
	if c.App.SwaggerEnabled && c.IsProduction() {
		errs.Addf("SWAGGER_ENABLED: must not be true with APP_ENV=production")
	}
	// vogel:component bpms begin
	if _, ok := c.Storages[c.BPMS.DocumentStorage]; !ok {
		errs.Addf("BPMS_DOCUMENT_STORAGE: %q is not listed in STORAGE_CONFIGS", c.BPMS.DocumentStorage)
	}
	// vogel:component bpms end
}

func (c *Config) IsProduction() bool {
	return c.App.Env == "production"
}

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

func parseTrustedProxies(errs *config.Errors) []netip.Prefix {
	prefixes, err := vmw.ParseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		errs.Addf("TRUSTED_PROXIES: %v", err)
		return nil
	}
	return prefixes
}

const (
	defaultAcquireTimeout = 3 * time.Second
	defaultLeakThreshold  = 10 * time.Second
)

func positiveDuration(errs *config.Errors, key string, def time.Duration) time.Duration {
	v := errs.Duration(key, def)
	if v <= 0 {
		errs.Addf("%s: must be greater than zero", key)
		return def
	}
	return v
}

func parseSwaggerEnabled(errs *config.Errors) bool {
	return parseBool(errs, "SWAGGER_ENABLED", false)
}

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

const defaultMaxTokenLifetime = 12 * time.Hour

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
func WorkerSchema() string {
	return config.String("WORKER_SCHEMA", "river")
}

// vogel:component worker end
func MigrateRequiresTLS() (bool, error) {
	var errs config.Errors
	v := parseBool(&errs, "DB_REQUIRE_TLS", true)
	return v, errs.Err()
}

// vogel:component worker begin
func parseWorkerConfig(errs *config.Errors) worker.Config {
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
		PollOnly:          parseBool(errs, "WORKER_POLL_ONLY", false),
	}
}

// vogel:component worker end
// vogel:component storage begin
const (
	defaultPresignExpiry = 15 * time.Minute
	maxPresignExpiry     = time.Hour
)

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
// vogel:component tracing begin
func parseTracingConfig(errs *config.Errors, env, version string) otel.Config {
	const (
		endpointKey = "OTEL_EXPORTER_OTLP_ENDPOINT"
		ratioKey    = "OTEL_TRACES_SAMPLER_ARG"
	)
	cfg := otel.Config{
		Endpoint:       config.String(endpointKey, ""),
		ServiceName:    config.String("OTEL_SERVICE_NAME", ""),
		ServiceVersion: version,
		Environment:    env,
	}
	if cfg.Endpoint != "" {
		if u, err := url.Parse(cfg.Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs.Addf("%s: must be an http:// or https:// URL with a host", endpointKey)
		}
	}
	if raw := config.String(ratioKey, ""); raw != "" {
		ratio, err := strconv.ParseFloat(raw, 64)
		if err != nil || !(ratio > 0 && ratio <= 1) {
			errs.Addf("%s: must be a number greater than 0 and at most 1 (to turn tracing off, leave %s empty)", ratioKey, endpointKey)
		} else {
			cfg.SampleRatio = ratio
		}
	}
	return cfg
}

// vogel:component tracing end
// vogel:component bpms begin
const defaultDocumentStorage = "documents"

func parseBPMSConfig(errs *config.Errors) BPMSConfig {
	cfg := BPMSConfig{
		DocumentStorage: config.String("BPMS_DOCUMENT_STORAGE", defaultDocumentStorage),
		Gotenberg: gotenberg.Config{
			BaseURL: errs.Require("GOTENBERG_URL"),
			Timeout: positiveDuration(errs, "GOTENBERG_TIMEOUT", gotenberg.DefaultTimeout),
		},
	}
	if cfg.Gotenberg.BaseURL != "" {
		if _, err := gotenberg.New(cfg.Gotenberg); err != nil {
			errs.Addf("GOTENBERG_URL: %v", err)
		}
	}
	return cfg
}

// vogel:component bpms end
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
func parseMetricsConfig() MetricsConfig {
	return MetricsConfig{
		ListenAddr: config.String("METRICS_LISTEN_ADDR", ":9090"),
		// vogel:component worker begin

		WorkerListenAddr: config.String("WORKER_METRICS_LISTEN_ADDR", ":9091"),
		// vogel:component worker end
	}
}
