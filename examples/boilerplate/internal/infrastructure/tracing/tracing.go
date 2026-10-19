// Package tracing starts and stops this service's OpenTelemetry tracing
// (vogel/otel) for both binaries. Tracing is opt-in at runtime: without
// OTEL_EXPORTER_OTLP_ENDPOINT, Setup installs nothing and every span is a
// no-op. Generate the service with --without tracing to drop it entirely.
package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kafeiih/vogel/otel"
)

// shutdownTimeout bounds the flush of the spans still buffered at shutdown.
const shutdownTimeout = 10 * time.Second

// Setup installs the global tracer provider for cfg, naming the service
// after the binary when OTEL_SERVICE_NAME left cfg.ServiceName empty.
func Setup(ctx context.Context, cfg otel.Config, binary string, logger *slog.Logger) (*otel.Provider, error) {
	if cfg.ServiceName == "" {
		cfg.ServiceName = binary
	}
	tp, err := otel.Setup(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("setting up tracing: %w", err)
	}
	if !tp.Enabled() {
		logger.Info("tracing disabled: OTEL_EXPORTER_OTLP_ENDPOINT is not set")
		return tp, nil
	}
	// The endpoint is not logged: its userinfo, if any, is a credential.
	logger.Info("tracing enabled",
		"service", cfg.ServiceName,
		"sample_ratio", sampleRatio(cfg),
	)
	return tp, nil
}

// Shutdown flushes the buffered spans, bounded by shutdownTimeout. Call it
// last, after the HTTP server and the job queue have stopped, so their final
// spans are exported too.
func Shutdown(tp *otel.Provider, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := tp.Shutdown(ctx); err != nil {
		logger.Error("flushing traces at shutdown failed", "error", err)
	}
}

func sampleRatio(cfg otel.Config) float64 {
	if cfg.SampleRatio == 0 {
		return otel.DefaultSampleRatio
	}
	return cfg.SampleRatio
}
