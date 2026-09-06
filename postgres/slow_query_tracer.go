package postgres

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

type contextKey struct{}

// SlowQueryTracer implements pgx.QueryTracer and logs queries that exceed the configured threshold.
type SlowQueryTracer struct {
	threshold time.Duration
	logger    *slog.Logger
}

// NewSlowQueryTracer creates a tracer that logs queries slower than threshold.
func NewSlowQueryTracer(logger *slog.Logger, threshold time.Duration) *SlowQueryTracer {
	return &SlowQueryTracer{
		threshold: threshold,
		logger:    logger,
	}
}

func (t *SlowQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, contextKey{}, &queryTrace{
		start: time.Now(),
		sql:   data.SQL,
		args:  len(data.Args),
	})
}

func (t *SlowQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	trace, ok := ctx.Value(contextKey{}).(*queryTrace)
	if !ok {
		return
	}

	elapsed := time.Since(trace.start)
	if elapsed < t.threshold {
		return
	}

	attrs := []any{
		"duration", elapsed,
		"sql", trace.sql,
		"args_count", trace.args,
	}
	if data.Err != nil {
		attrs = append(attrs, "error", data.Err.Error())
	}

	t.logger.Warn("slow query detected", attrs...)
}

type queryTrace struct {
	start time.Time
	sql   string
	args  int
}
