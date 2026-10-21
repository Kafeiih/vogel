package main

import (
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/kafeiih/vogel/config"
)

// Config holds every setting this example needs to boot. It is intentionally
// flat and specific to this binary: vogel/config deliberately does not
// define application config structs itself (see its package doc comment),
// so shaping one like this is always the consuming application's job.
type Config struct {
	Env             string
	Port            int
	DatabaseURL     string
	RiverSchema     string
	NotifyFrom      string
	ShutdownTimeout time.Duration

	// MetricsAddr (METRICS_ADDR, default 127.0.0.1:2112) is where the
	// internal listener serves /metrics. It is separate from the public
	// PORT so Prometheus data is never exposed on the API port; the
	// loopback default keeps it off the network until an operator opts
	// in (for example ":2112" inside a container scraped by Prometheus).
	// 2112 rather than 9090, which is Prometheus's own default port.
	MetricsAddr string

	// HTTP server timeouts for the public API listener (see newAPIServer).
	// Each defaults to a finite value and must be positive: in net/http a
	// zero timeout means none at all, which lets a slow client hold a
	// connection, and the goroutine serving it, open indefinitely.
	ReadHeaderTimeout time.Duration // HTTP_READ_HEADER_TIMEOUT, default 5s
	ReadTimeout       time.Duration // HTTP_READ_TIMEOUT, default 15s
	WriteTimeout      time.Duration // HTTP_WRITE_TIMEOUT, default 30s
	IdleTimeout       time.Duration // HTTP_IDLE_TIMEOUT, default 60s

	// Per-client-IP request budget for the authenticated routes (see ipRateLimiter):
	// RateLimitRequests (RATE_LIMIT_REQUESTS, default 300) per
	// RateLimitWindow (RATE_LIMIT_WINDOW, default 1m). Both must be positive.
	RateLimitRequests int
	RateLimitWindow   time.Duration

	// FakeStorageBaseURL (FAKE_STORAGE_BASE_URL, default
	// http://localhost:<PORT>) is the origin the fake bucket's presigned
	// URLs point at: where a client reaches this server's /fake-storage/
	// endpoint. It must be an absolute http or https URL. A real adapter
	// (storage/s3) needs no such setting: its URLs point at the bucket.
	FakeStorageBaseURL string
}

// LoadConfig reads every setting from the environment and validates it
// before returning.
//
// It accumulates every problem found with a config.Errors instead of
// returning on the first one encountered. That accumulator exists precisely
// so a single boot failure reports every missing or invalid variable at
// once: without it, an operator fixes one variable, restarts, discovers the
// next missing one, and repeats -- one restart per typo. Errors.Err()
// collects the whole list into a single error returned here.
func LoadConfig() (Config, error) {
	var errs config.Errors

	databaseURL := errs.Require("DATABASE_URL")

	env := config.String("ENV", "development")
	errs.OneOf("ENV", env, "development", "staging", "production")

	port := errs.Int("PORT", 8080)
	errs.IntRange("PORT", port, 1, 65535)

	shutdownTimeout := errs.Duration("SHUTDOWN_TIMEOUT", 15*time.Second)

	riverSchema := config.String("RIVER_SCHEMA", "river")
	notifyFrom := config.String("NOTIFY_FROM", "noreply@example.com")
	metricsAddr := config.String("METRICS_ADDR", "127.0.0.1:2112")
	if strings.TrimSpace(metricsAddr) == "" {
		errs.Addf("METRICS_ADDR: must not be empty")
	}

	readHeaderTimeout := positiveDuration(&errs, "HTTP_READ_HEADER_TIMEOUT", 5*time.Second)
	readTimeout := positiveDuration(&errs, "HTTP_READ_TIMEOUT", 15*time.Second)
	writeTimeout := positiveDuration(&errs, "HTTP_WRITE_TIMEOUT", 30*time.Second)
	idleTimeout := positiveDuration(&errs, "HTTP_IDLE_TIMEOUT", 60*time.Second)

	rateLimitRequests := errs.Int("RATE_LIMIT_REQUESTS", 300)
	errs.IntRange("RATE_LIMIT_REQUESTS", rateLimitRequests, 1, math.MaxInt32)
	rateLimitWindow := positiveDuration(&errs, "RATE_LIMIT_WINDOW", time.Minute)

	fakeStorageBaseURL := config.String("FAKE_STORAGE_BASE_URL", fmt.Sprintf("http://localhost:%d", port))
	if u, err := url.Parse(fakeStorageBaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs.Addf("FAKE_STORAGE_BASE_URL: must be an absolute http or https URL, got %q", fakeStorageBaseURL)
	}

	if err := errs.Err(); err != nil {
		return Config{}, err
	}

	return Config{
		Env:             env,
		Port:            port,
		DatabaseURL:     databaseURL,
		RiverSchema:     riverSchema,
		NotifyFrom:      notifyFrom,
		ShutdownTimeout: shutdownTimeout,
		MetricsAddr:     metricsAddr,

		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,

		RateLimitRequests: rateLimitRequests,
		RateLimitWindow:   rateLimitWindow,

		FakeStorageBaseURL: fakeStorageBaseURL,
	}, nil
}

// positiveDuration reads key as a duration (defaultValue when unset) and
// records an error on errs unless it is greater than zero.
func positiveDuration(errs *config.Errors, key string, defaultValue time.Duration) time.Duration {
	d := errs.Duration(key, defaultValue)
	if d <= 0 {
		errs.Addf("%s: must be greater than zero, got %s", key, d)
	}
	return d
}
