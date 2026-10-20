package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kafeiih/vogel/otel"
)

const shutdownTimeout = 10 * time.Second

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
	logger.Info("tracing enabled",
		"service", cfg.ServiceName,
		"sample_ratio", sampleRatio(cfg),
	)
	return tp, nil
}

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
