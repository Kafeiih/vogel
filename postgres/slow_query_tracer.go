package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type contextKey struct{}

// maxLoggedSQLBytes bounds the SQL text a slow-query line carries. Longer
// statements are cut at a rune boundary and flagged with sql_truncated, with
// sql_bytes giving the full length.
const maxLoggedSQLBytes = 2048

// SlowQueryTracer implements pgx.QueryTracer and logs queries that exceed the configured threshold.
//
// Bind values are never logged, only their count (args_count). The SQL text
// is logged, bounded by maxLoggedSQLBytes, because a slow query cannot be
// diagnosed without it; a literal inlined in the SQL instead of bound would
// therefore reach the log. WithSlowQuerySQLHash replaces the text with a
// short hash (sql_hash) that still groups repeated queries.
type SlowQueryTracer struct {
	threshold time.Duration
	logger    *slog.Logger
	hashSQL   bool
}

// SlowQueryOption configures a SlowQueryTracer.
type SlowQueryOption func(*SlowQueryTracer)

// WithSlowQuerySQLHash logs sql_hash, the first 8 bytes of the SHA-256 of the
// SQL text in hex, instead of the text itself.
func WithSlowQuerySQLHash() SlowQueryOption {
	return func(t *SlowQueryTracer) { t.hashSQL = true }
}

// NewSlowQueryTracer creates a tracer that logs queries slower than threshold.
func NewSlowQueryTracer(logger *slog.Logger, threshold time.Duration, opts ...SlowQueryOption) *SlowQueryTracer {
	t := &SlowQueryTracer{
		threshold: threshold,
		logger:    logger,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
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

	attrs := []any{"duration", elapsed}
	attrs = append(attrs, t.sqlAttrs(trace.sql)...)
	attrs = append(attrs, "args_count", trace.args)
	if data.Err != nil {
		attrs = append(attrs, "error", data.Err.Error())
	}

	t.logger.Warn("slow query detected", attrs...)
}

// sqlAttrs returns the log attributes describing sql: its hash, or its text
// bounded by maxLoggedSQLBytes.
func (t *SlowQueryTracer) sqlAttrs(sql string) []any {
	if t.hashSQL {
		sum := sha256.Sum256([]byte(sql))
		return []any{"sql_hash", hex.EncodeToString(sum[:8])}
	}
	if len(sql) <= maxLoggedSQLBytes {
		return []any{"sql", sql}
	}
	cut := maxLoggedSQLBytes
	for cut > 0 && !utf8.RuneStart(sql[cut]) {
		cut--
	}
	return []any{"sql", sql[:cut], "sql_truncated", true, "sql_bytes", len(sql)}
}

type queryTrace struct {
	start time.Time
	sql   string
	args  int
}
