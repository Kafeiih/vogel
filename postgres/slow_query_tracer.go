package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kafeiih/vogel/internal/logtext"
)

type contextKey struct{}

// maxLoggedSQLBytes bounds the SQL text a slow-query line carries. Longer
// statements are cut at a rune boundary and flagged with sql_truncated, with
// sql_bytes giving the full length.
const maxLoggedSQLBytes = 2048

// maxLoggedErrorBytes bounds a failed query's error text; a longer one is cut
// at a rune boundary and flagged with error_truncated.
const maxLoggedErrorBytes = 1024

// sqlHashBytes is how many bytes of the SHA-256 of the SQL text sql_hash
// carries, hex encoded (so twice as many characters).
const sqlHashBytes = 8

// SlowQueryTracer implements pgx.QueryTracer and logs queries that exceed the configured threshold.
//
// Bind values are never logged, only their count (args_count). The SQL text
// is logged, bounded by maxLoggedSQLBytes, because a slow query cannot be
// diagnosed without it; a literal inlined in the SQL instead of bound would
// therefore reach the log. WithSlowQuerySQLHash replaces the text with a
// short hash (sql_hash) that still groups repeated queries.
//
// A failed query's error text is logged bounded by maxLoggedErrorBytes. An
// error message can quote a value: PostgreSQL's (`invalid input syntax for
// type uuid: "..."`) and pgx's own when it encodes an argument or scans a
// result. So with WithSlowQuerySQLHash the text is dropped: a
// *pgconn.PgError is logged as error_code (its SQLSTATE) and
// error_constraint, a canceled or timed-out context keeps its fixed text, and
// any other error is logged by its Go type only (error_type).
//
// The hash is unsalted, so it pseudonymizes rather than hides: someone who
// can read the logs and knows the query template can hash candidate values
// (an email, a user ID) and confirm a low-entropy literal. Binding values as
// parameters is what keeps them out of the logs; the hash only keeps the
// statement text out.
type SlowQueryTracer struct {
	threshold time.Duration
	logger    *slog.Logger
	hashSQL   bool
}

// SlowQueryOption configures a SlowQueryTracer.
type SlowQueryOption func(*SlowQueryTracer)

// WithSlowQuerySQLHash logs sql_hash, the first sqlHashBytes bytes of the
// SHA-256 of the SQL text in hex, instead of the text itself, and a
// PostgreSQL error by its SQLSTATE instead of its message.
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
		attrs = append(attrs, t.errorAttrs(data.Err)...)
	}

	// WarnContext lets a context-aware handler (logger.New's) add the
	// request_id, and the trace IDs of a span opened by Config.Tracer.
	t.logger.WarnContext(ctx, "slow query detected", attrs...)
}

// sqlAttrs returns the log attributes describing sql: its hash, or its text
// bounded by maxLoggedSQLBytes.
func (t *SlowQueryTracer) sqlAttrs(sql string) []any {
	if t.hashSQL {
		sum := sha256.Sum256([]byte(sql))
		return []any{"sql_hash", hex.EncodeToString(sum[:sqlHashBytes])}
	}
	text, truncated := logtext.Truncate(sql, maxLoggedSQLBytes)
	if !truncated {
		return []any{"sql", text}
	}
	return []any{"sql", text, "sql_truncated", true, "sql_bytes", len(sql)}
}

// errorAttrs returns the log attributes describing a failed query's err.
func (t *SlowQueryTracer) errorAttrs(err error) []any {
	if !t.hashSQL {
		text, truncated := logtext.Truncate(err.Error(), maxLoggedErrorBytes)
		if !truncated {
			return []any{"error", text}
		}
		return []any{"error", text, "error_truncated", true}
	}
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr):
		attrs := []any{"error_code", pgErr.Code}
		if pgErr.ConstraintName != "" {
			attrs = append(attrs, "error_constraint", pgErr.ConstraintName)
		}
		return attrs
	case errors.Is(err, context.Canceled):
		return []any{"error", context.Canceled.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		return []any{"error", context.DeadlineExceeded.Error()}
	default:
		return []any{"error_type", fmt.Sprintf("%T", err)}
	}
}

type queryTrace struct {
	start time.Time
	sql   string
	args  int
}
