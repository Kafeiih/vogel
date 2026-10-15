package main

import (
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
	}, nil
}
