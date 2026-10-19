// Package otelpgx records an OpenTelemetry span for every pgx query, batch,
// COPY FROM and connection attempt. Plug its Tracer into
// postgres.Config.Tracer, which calls it alongside vogel's own pool tracer:
//
//	pool, err := postgres.NewPool(ctx, postgres.Config{
//		URL:    dsn,
//		Tracer: otelpgx.NewTracer(),
//	}, logger)
//
// A span carries the SQL text (db.query.text), never the query arguments,
// the same rule as postgres's SlowQueryTracer: arguments are where user data
// and secrets live. SQL that inlines values instead of passing them as $n
// arguments puts those values in the span; pass them as arguments. Error
// messages are never recorded either, since they can quote the rejected
// value: a Postgres error records its SQLSTATE (db.response.status_code),
// any other error its Go type (error.type).
package otelpgx

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"weak"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	gootel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/kafeiih/vogel/otel/otelpgx"

// Option configures a Tracer.
type Option func(*Tracer)

// WithTracerProvider sets the TracerProvider the Tracer uses instead of the
// global one otel.Setup installs.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(t *Tracer) { t.tp = tp }
}

// Tracer implements pgx.QueryTracer, pgx.BatchTracer, pgx.CopyFromTracer and
// pgx.ConnectTracer. Pool acquisition and statement preparation get no span
// of their own: they are already covered by the query span that follows.
type Tracer struct {
	tp trace.TracerProvider // nil: the global one, read on every span

	// conns caches each connection's server and database attributes, keyed
	// weakly so a closed connection's entry is dropped once it is collected:
	// pgx.Conn.Config copies the whole config, too costly per query.
	conns sync.Map // weak.Pointer[pgx.Conn] -> []attribute.KeyValue
}

var (
	_ pgx.QueryTracer    = (*Tracer)(nil)
	_ pgx.BatchTracer    = (*Tracer)(nil)
	_ pgx.CopyFromTracer = (*Tracer)(nil)
	_ pgx.ConnectTracer  = (*Tracer)(nil)
)

// NewTracer returns a Tracer. The global TracerProvider is read when each
// span starts, not here, so NewTracer may run before otel.Setup.
func NewTracer(opts ...Option) *Tracer {
	t := &Tracer{}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

func (t *Tracer) tracer() trace.Tracer {
	tp := t.tp
	if tp == nil {
		tp = gootel.GetTracerProvider()
	}
	return tp.Tracer(instrumentationName)
}

func (t *Tracer) start(ctx context.Context, name string, conn []attribute.KeyValue, attrs ...attribute.KeyValue) context.Context {
	// A fresh slice: conn is shared through the cache and must not be
	// appended to.
	all := make([]attribute.KeyValue, 0, len(attrs)+len(conn)+1)
	attrs = append(append(append(all, attrs...), semconv.DBSystemNamePostgreSQL), conn...)
	ctx, _ = t.tracer().Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...))
	return ctx
}

// endSpan ends the span started by start, recording err. pgx.ErrNoRows is
// a normal outcome of QueryRow, not a failure.
func endSpan(ctx context.Context, err error) {
	span := trace.SpanFromContext(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		recordError(span, err)
	}
	span.End()
}

// recordError marks span as failed by err, by the rule postgres's
// SlowQueryTracer follows with HashSlowQuerySQL: a Postgres error by its
// SQLSTATE, a context error by its fixed text, any other error by its Go
// type. Error messages are never recorded: Postgres's can quote the value it
// rejected ("invalid input syntax for type integer: ...") and pgx's the
// argument it failed to encode, which is query data like the arguments.
func recordError(span trace.Span, err error) {
	if code := sqlState(err); code != "" {
		span.SetAttributes(semconv.DBResponseStatusCode(code))
		span.SetStatus(codes.Error, "SQLSTATE "+code)
		return
	}
	desc := errorDescription(err)
	span.SetAttributes(semconv.ErrorTypeKey.String(desc))
	span.SetStatus(codes.Error, desc)
}

// errorDescription describes a non-Postgres error without its message: the
// fixed text of a context error, or the Go type of the innermost error.
func errorDescription(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded.Error()
	}
	for next := errors.Unwrap(err); next != nil; next = errors.Unwrap(next) {
		err = next
	}
	return fmt.Sprintf("%T", err)
}

// sqlState returns the SQLSTATE of the Postgres error in err's chain, or "".
func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func (t *Tracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	op := operationName(data.SQL)
	return t.start(ctx, spanName(op), t.connAttrsOf(conn), queryAttrs(op, data.SQL)...)
}

func (t *Tracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	endSpan(ctx, data.Err)
}

func (t *Tracer) TraceBatchStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	var attrs []attribute.KeyValue
	if data.Batch != nil {
		attrs = append(attrs, semconv.DBOperationBatchSize(data.Batch.Len()))
	}
	return t.start(ctx, "BATCH", t.connAttrsOf(conn), append(attrs, semconv.DBOperationName("BATCH"))...)
}

// TraceBatchQuery records each query of the batch as an event on the batch
// span, so a batch of a thousand inserts stays one span.
func (t *Tracer) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	span := trace.SpanFromContext(ctx)
	attrs := queryAttrs(operationName(data.SQL), data.SQL)
	if code := sqlState(data.Err); code != "" {
		attrs = append(attrs, semconv.DBResponseStatusCode(code))
	} else if data.Err != nil {
		attrs = append(attrs, semconv.ErrorTypeKey.String(errorDescription(data.Err)))
	}
	span.AddEvent("query", trace.WithAttributes(attrs...))
}

func (t *Tracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	endSpan(ctx, data.Err)
}

func (t *Tracer) TraceCopyFromStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	table := data.TableName.Sanitize()
	return t.start(ctx, "COPY "+table, t.connAttrsOf(conn),
		semconv.DBOperationName("COPY"), semconv.DBCollectionName(table))
}

func (t *Tracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	endSpan(ctx, data.Err)
}

func (t *Tracer) TraceConnectStart(ctx context.Context, data pgx.TraceConnectStartData) context.Context {
	var conn []attribute.KeyValue
	if data.ConnConfig != nil {
		conn = connAttrs(data.ConnConfig)
	}
	return t.start(ctx, "connect", conn)
}

func (t *Tracer) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	endSpan(ctx, data.Err)
}

// connAttrsOf returns conn's server and database attributes, computed once
// per connection.
func (t *Tracer) connAttrsOf(conn *pgx.Conn) []attribute.KeyValue {
	if conn == nil {
		return nil
	}
	key := weak.Make(conn)
	if v, ok := t.conns.Load(key); ok {
		if attrs, ok := v.([]attribute.KeyValue); ok {
			return attrs
		}
	}
	attrs := connAttrs(conn.Config())
	if _, loaded := t.conns.LoadOrStore(key, attrs); !loaded {
		runtime.AddCleanup(conn, func(k weak.Pointer[pgx.Conn]) { t.conns.Delete(k) }, key)
	}
	return attrs
}

func connAttrs(cfg *pgx.ConnConfig) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 3)
	if cfg.Database != "" {
		attrs = append(attrs, semconv.DBNamespace(cfg.Database))
	}
	if cfg.Host != "" {
		attrs = append(attrs, semconv.ServerAddress(cfg.Host))
	}
	if cfg.Port != 0 {
		attrs = append(attrs, semconv.ServerPort(int(cfg.Port)))
	}
	return attrs
}

func queryAttrs(op, sql string) []attribute.KeyValue {
	attrs := []attribute.KeyValue{semconv.DBQueryText(sql)}
	if op != "" {
		attrs = append(attrs, semconv.DBOperationName(op))
	}
	return attrs
}

// spanName is the query's operation, or "postgresql" when the SQL has none
// recognizable: SQL text itself is too long and too high-cardinality to be a
// span name.
func spanName(op string) string {
	if op == "" {
		return "postgresql"
	}
	return op
}

// operationName returns the SQL's leading keyword in upper case (SELECT,
// INSERT, WITH, ...), skipping leading whitespace and comments, or "" when
// it does not start with a word.
func operationName(sql string) string {
	sql = skipComments(sql)
	n := strings.IndexFunc(sql, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z')
	})
	if n == -1 {
		n = len(sql)
	}
	return strings.ToUpper(sql[:n])
}

func skipComments(sql string) string {
	for {
		sql = strings.TrimLeft(sql, " \t\r\n")
		switch {
		case strings.HasPrefix(sql, "--"):
			i := strings.IndexByte(sql, '\n')
			if i == -1 {
				return ""
			}
			sql = sql[i+1:]
		case strings.HasPrefix(sql, "/*"):
			i := strings.Index(sql, "*/")
			if i == -1 {
				return ""
			}
			sql = sql[i+2:]
		default:
			return sql
		}
	}
}
