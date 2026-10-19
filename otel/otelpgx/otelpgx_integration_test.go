//go:build integration

package otelpgx_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/kafeiih/vogel/otel/otelpgx"
	"github.com/kafeiih/vogel/postgres"
)

// startTestPostgres mirrors postgres/postgres_integration_test.go.
func startTestPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("test_db"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err, "start postgres container")
	t.Cleanup(func() { _ = pgContainer.Terminate(context.Background()) })

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return connStr
}

// Plugged into postgres.Config.Tracer, the Tracer gets every hook through
// vogel's pool tracer: a connect, a query, a batch and a COPY FROM each end
// one span, all children of the caller's span except the connection.
func TestTracer_ThroughNewPool(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, postgres.Config{
		URL:    startTestPostgres(t),
		Tracer: otelpgx.NewTracer(otelpgx.WithTracerProvider(tp)),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	reqCtx, req := tp.Tracer("test").Start(ctx, "request")
	_, err = pool.Exec(reqCtx, `CREATE TABLE events (id int)`)
	require.NoError(t, err)

	b := &pgx.Batch{}
	b.Queue(`INSERT INTO events VALUES (1)`)
	b.Queue(`INSERT INTO events VALUES (2)`)
	require.NoError(t, pool.SendBatch(reqCtx, b).Close())

	_, err = pool.CopyFrom(reqCtx, pgx.Identifier{"events"}, []string{"id"}, pgx.CopyFromRows([][]any{{3}}))
	require.NoError(t, err)

	var n int
	err = pool.QueryRow(reqCtx, `SELECT count(*) FROM events WHERE id = $1`, 42).Scan(&n)
	require.NoError(t, err)
	req.End()

	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		byName[s.Name()] = s
	}
	for _, name := range []string{"connect", "CREATE", "BATCH", `COPY "events"`, "SELECT"} {
		require.Contains(t, byName, name, "span %q", name)
	}
	for _, name := range []string{"CREATE", "BATCH", `COPY "events"`, "SELECT"} {
		assert.Equal(t, req.SpanContext().SpanID(), byName[name].Parent().SpanID(), "%s is a child of the request", name)
	}

	sel := map[string]string{}
	for _, kv := range byName["SELECT"].Attributes() {
		sel[string(kv.Key)] = kv.Value.String()
	}
	assert.Equal(t, `SELECT count(*) FROM events WHERE id = $1`, sel["db.query.text"])
	assert.Equal(t, "test_db", sel["db.namespace"])
	for k, v := range sel {
		assert.NotEqual(t, "42", v, "%s holds a query argument", k)
	}
}
