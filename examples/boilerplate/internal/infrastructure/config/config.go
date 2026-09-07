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
// needs field-for-field (postgres.Config, worker.Config, zitedel.Config,
// cerbos.Config, s3.Config, smtp.Config, sendgrid.Config), this package
// embeds that type directly instead of mirroring it with a redundant local
// struct that would need converting at the call site in main.go. Only the
// fields with no vogel equivalent (App, CORS, Metrics) get a local type.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/kafeiih/vogel/auth/zitadel"
	"github.com/kafeiih/vogel/authz/cerbos"
	"github.com/kafeiih/vogel/config"
	"github.com/kafeiih/vogel/notification/sendgrid"
	"github.com/kafeiih/vogel/notification/smtp"
	"github.com/kafeiih/vogel/postgres"
	"github.com/kafeiih/vogel/storage/s3"
	"github.com/kafeiih/vogel/worker"
)

// Config holds every setting this application needs to boot.
type Config struct {
	App          AppConfig
	Database     postgres.Config
	Zitadel      zitadel.Config
	Cerbos       cerbos.Config
	CORS         CORSConfig
	Worker       worker.Config
	Storages     map[string]s3.Config
	Notification NotificationConfig
	Metrics      MetricsConfig
}

// AppConfig holds settings with no vogel equivalent: this application's own
// identity and HTTP timeout.
type AppConfig struct {
	Env     string // development, staging, production
	Port    string // :8080
	Url     string // localhost:8080
	Version string
	Timeout time.Duration // 30s
}

// MetricsConfig holds configuration for the dedicated Prometheus metrics
// server. vogel does not run one of its own -- see metrics_server.go.
type MetricsConfig struct {
	ListenAddr string // METRICS_LISTEN_ADDR, default ":9090"
}

// NotificationConfig holds configuration for email delivery. Provider picks
// which of the two vogel-shaped configs below main.go actually builds an
// adapter from.
type NotificationConfig struct {
	Provider    string          // "smtp" (default) or "sendgrid"
	DefaultFrom string          // Default sender address, e.g. "noreply@institucion.cl"
	SMTP        smtp.Config     // SMTP-specific settings
	SendGrid    sendgrid.Config // SendGrid-specific settings
}

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
	zitadelIssuer := errs.Require("ZITADEL_ISSUER")
	zitadelClientID := errs.Require("ZITADEL_CLIENT_ID")

	maxConns := errs.Int32("DB_MAX_CONNS", 25)
	minConns := errs.Int32("DB_MIN_CONNS", 5)

	appTimeout := errs.Duration("APP_TIMEOUT", 30*time.Second)
	maxConnLifetime := errs.Duration("DB_MAX_CONN_LIFETIME", time.Hour)
	maxConnIdleTime := errs.Duration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute)
	connectTimeout := errs.Duration("DB_CONNECT_TIMEOUT", 10*time.Second)

	if err := errs.Err(); err != nil {
		return nil, err
	}

	env := config.String("APP_ENV", "development")

	cfg := &Config{
		App: AppConfig{
			Env:     env,
			Port:    config.String("APP_PORT", ":8080"),
			Url:     config.String("APP_URL", "localhost:8080"),
			Version: config.String("APP_VERSION", "0.1.0"),
			Timeout: appTimeout,
		},
		Database: postgres.Config{
			URL:             dbURL,
			MaxConns:        maxConns,
			MinConns:        minConns,
			MaxConnLifetime: maxConnLifetime,
			MaxConnIdleTime: maxConnIdleTime,
			ConnectTimeout:  connectTimeout,
			RequireTLS:      env != "development",
		},
		Zitadel: zitadel.Config{
			Issuer:   zitadelIssuer,
			ClientID: zitadelClientID,
		},
		Cerbos: cerbos.Config{
			Host:   config.String("CERBOS_HOST", "localhost:3593"),
			UseTLS: config.Bool("CERBOS_TLS", false),
		},
		CORS: CORSConfig{
			AllowedOrigins: parseSlice("CORS_ALLOWED_ORIGINS", ","),
			AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"},
			AllowedHeaders: []string{"Authorization", "Content-Type", "X-Request-ID"},
		},
		Worker:       parseWorkerConfig(),
		Storages:     parseStorageConfigs(),
		Notification: parseNotificationConfig(),
		Metrics:      parseMetricsConfig(),
	}

	return cfg, nil
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

// parseWorkerConfig reads WORKER_* env vars and returns a worker.Config with
// defaults.
func parseWorkerConfig() worker.Config {
	schema := config.String("WORKER_SCHEMA", "river")

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

// parseStorageConfigs reads named storage configurations from environment
// variables.
//
// Format:
//
//	STORAGE_CONFIGS=public,private
//	STORAGE_PUBLIC_BUCKET=cdn-assets
//	STORAGE_PUBLIC_REGION=us-east-1
//	STORAGE_PRIVATE_BUCKET=documentos
//	STORAGE_PRIVATE_ENDPOINT=http://minio:9000
//	STORAGE_PRIVATE_PATH_STYLE=true
//
// Returns an empty map if STORAGE_CONFIGS is not set.
func parseStorageConfigs() map[string]s3.Config {
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

		presignExpiry := 15 * time.Minute
		if v := os.Getenv(prefix + "PRESIGN_EXPIRY"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				presignExpiry = d
			}
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

// parseMetricsConfig reads METRICS_* env vars and returns a MetricsConfig
// with defaults.
func parseMetricsConfig() MetricsConfig {
	return MetricsConfig{
		ListenAddr: config.String("METRICS_LISTEN_ADDR", ":9090"),
	}
}

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
