package otelpgx

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/kafeiih/vogel/postgres"
)

var _ = postgres.Config{Tracer: NewTracer()}

func newRecorder(t *testing.T) (*Tracer, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return NewTracer(WithTracerProvider(tp)), rec
}

func attrs(s sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	m := map[attribute.Key]attribute.Value{}
	for _, kv := range s.Attributes() {
		m[kv.Key] = kv.Value
	}
	return m
}

func TestQuery_RecordsAClientSpanWithTheSQLText(t *testing.T) {
	tr, rec := newRecorder(t)
	const sql = "/* by id */ select * from users where id = $1"

	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: sql, Args: []any{"secret-arg"}})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	spans := rec.Ended()
	require.Len(t, spans, 1)
	s := spans[0]
	assert.Equal(t, "SELECT", s.Name())
	assert.Equal(t, trace.SpanKindClient, s.SpanKind())
	a := attrs(s)
	assert.Equal(t, "postgresql", a["db.system.name"].AsString())
	assert.Equal(t, sql, a["db.query.text"].AsString())
	assert.Equal(t, "SELECT", a["db.operation.name"].AsString())
	assert.Equal(t, codes.Unset, s.Status().Code)
	for _, kv := range s.Attributes() {
		assert.NotContains(t, kv.Value.String(), "secret-arg", "query arguments must never be recorded")
	}
}

func TestQuery_RecordsTheError(t *testing.T) {
	tr, rec := newRecorder(t)

	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "insert into t values (1)"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: errors.New("duplicate key")})

	s := rec.Ended()[0]
	assert.Equal(t, "INSERT", s.Name())
	assert.Equal(t, codes.Error, s.Status().Code)
	assert.Equal(t, "*errors.errorString", s.Status().Description, "the message is never recorded")
	assert.Empty(t, s.Events(), "no exception event carrying the message")
}

func TestQuery_NoRowsIsNotAnError(t *testing.T) {
	tr, rec := newRecorder(t)

	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "select 1"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: pgx.ErrNoRows})
	ctx = tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "select 2"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: fmt.Errorf("find user: %w", pgx.ErrNoRows)})

	for _, s := range rec.Ended() {
		assert.Equal(t, codes.Unset, s.Status().Code, "%s: ErrNoRows, wrapped or not, is not a failure", s.Name())
	}
}

func TestQuery_IsAChildOfTheCallersSpan(t *testing.T) {
	tr, rec := newRecorder(t)
	parentCtx, parent := tr.tp.Tracer("test").Start(context.Background(), "parent")

	ctx := tr.TraceQueryStart(parentCtx, nil, pgx.TraceQueryStartData{SQL: "select 1"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	parent.End()

	q := rec.Ended()[0]
	assert.Equal(t, parent.SpanContext().SpanID(), q.Parent().SpanID())
	assert.Equal(t, parent.SpanContext().TraceID(), q.SpanContext().TraceID())
}

func TestBatch_IsOneSpanWithAnEventPerQuery(t *testing.T) {
	tr, rec := newRecorder(t)
	b := &pgx.Batch{}
	b.Queue("insert into t values (1)")
	b.Queue("update t set x = 2")

	ctx := tr.TraceBatchStart(context.Background(), nil, pgx.TraceBatchStartData{Batch: b})
	tr.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{SQL: "insert into t values (1)"})
	tr.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{SQL: "update t set x = 2", Err: errors.New("boom")})
	tr.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{Err: errors.New("boom")})

	spans := rec.Ended()
	require.Len(t, spans, 1)
	s := spans[0]
	assert.Equal(t, "BATCH", s.Name())
	assert.Equal(t, int64(2), attrs(s)["db.operation.batch.size"].AsInt64())
	assert.Equal(t, codes.Error, s.Status().Code)

	var queries []string
	for _, e := range s.Events() {
		if e.Name == "query" {
			for _, kv := range e.Attributes {
				if kv.Key == "db.query.text" {
					queries = append(queries, kv.Value.AsString())
				}
			}
		}
	}
	assert.Equal(t, []string{"insert into t values (1)", "update t set x = 2"}, queries)
}

func TestCopyFrom_NamesTheTable(t *testing.T) {
	tr, rec := newRecorder(t)

	ctx := tr.TraceCopyFromStart(context.Background(), nil, pgx.TraceCopyFromStartData{TableName: pgx.Identifier{"public", "events"}})
	tr.TraceCopyFromEnd(ctx, nil, pgx.TraceCopyFromEndData{})

	s := rec.Ended()[0]
	assert.Equal(t, `COPY "public"."events"`, s.Name())
	assert.Equal(t, `"public"."events"`, attrs(s)["db.collection.name"].AsString())
}

func TestConnect_CarriesTheServerAndDatabase(t *testing.T) {
	tr, rec := newRecorder(t)
	cfg, err := pgx.ParseConfig("postgres://u:hunter2@db.internal:6432/app")
	require.NoError(t, err)

	ctx := tr.TraceConnectStart(context.Background(), pgx.TraceConnectStartData{ConnConfig: cfg})
	tr.TraceConnectEnd(ctx, pgx.TraceConnectEndData{Err: errors.New("refused")})

	s := rec.Ended()[0]
	assert.Equal(t, "connect", s.Name())
	a := attrs(s)
	assert.Equal(t, "app", a["db.namespace"].AsString())
	assert.Equal(t, "db.internal", a["server.address"].AsString())
	assert.Equal(t, int64(6432), a["server.port"].AsInt64())
	assert.Equal(t, codes.Error, s.Status().Code)
	for _, kv := range s.Attributes() {
		assert.NotContains(t, kv.Value.String(), "hunter2", "the password must never be recorded")
	}
}

func TestOperationName(t *testing.T) {
	tests := map[string]string{
		"select 1":                      "SELECT",
		"  \n\tUPDATE t SET x = 1":      "UPDATE",
		"-- note\nwith x as (select 1)": "WITH",
		"/* a */ /* b */delete from t":  "DELETE",
		"select(1)":                     "SELECT",
		"":                              "",
		"-- only a comment":             "",
		"/* unterminated":               "",
		"(select 1)":                    "",
	}
	for sql, want := range tests {
		assert.Equal(t, want, operationName(sql), "operationName(%q)", sql)
	}
}

func TestSpanName_FallsBackToTheSystem(t *testing.T) {
	tr, rec := newRecorder(t)

	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "(select 1)"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	s := rec.Ended()[0]
	assert.Equal(t, "postgresql", s.Name())
	_, hasOp := attrs(s)["db.operation.name"]
	assert.False(t, hasOp)
}

// A Postgres error message can quote the offending value (invalid input
// syntax, check violations), so only its SQLSTATE reaches the span.
func TestQuery_PgErrorRecordsOnlyItsCode(t *testing.T) {
	tr, rec := newRecorder(t)
	pgErr := &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type integer: "hunter2"`}

	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "select $1::int"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: fmt.Errorf("lookup: %w", pgErr)})
	b := &pgx.Batch{}
	b.Queue("select $1::int")
	ctx = tr.TraceBatchStart(context.Background(), nil, pgx.TraceBatchStartData{Batch: b})
	tr.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{SQL: "select $1::int", Err: pgErr})
	tr.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{Err: pgErr})

	for _, s := range rec.Ended() {
		assert.Equal(t, codes.Error, s.Status().Code)
		assert.Equal(t, "SQLSTATE 22P02", s.Status().Description)
		assert.Equal(t, "22P02", attrs(s)["db.response.status_code"].AsString())
		for _, e := range s.Events() {
			for _, kv := range e.Attributes {
				assert.NotContains(t, kv.Value.String(), "hunter2", "%s event leaks the error message", s.Name())
			}
		}
	}
}

type encodeError struct{ msg string }

func (e *encodeError) Error() string { return e.msg }

// Any other error is recorded by its Go type: pgx's own messages (failing to
// encode an argument, for one) can quote the value. Context errors keep
// their text, which is fixed.
func TestQuery_NonPgErrorRecordsOnlyItsType(t *testing.T) {
	tr, rec := newRecorder(t)

	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "select $1"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: fmt.Errorf("query: %w", &encodeError{`unable to encode "hunter2"`})})
	ctx = tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "select 1"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: fmt.Errorf("query: %w", context.DeadlineExceeded)})

	spans := rec.Ended()
	require.Len(t, spans, 2)
	assert.Equal(t, "*otelpgx.encodeError", spans[0].Status().Description)
	assert.Equal(t, "*otelpgx.encodeError", attrs(spans[0])["error.type"].AsString())
	assert.Equal(t, "context deadline exceeded", spans[1].Status().Description)
	for _, s := range spans {
		assert.Equal(t, codes.Error, s.Status().Code)
		for _, e := range s.Events() {
			for _, kv := range e.Attributes {
				assert.NotContains(t, kv.Value.String(), "hunter2")
			}
		}
	}
}
