//go:build integration

package otelriver_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/kafeiih/vogel/otel/otelriver"
	"github.com/kafeiih/vogel/reqctx"
	"github.com/kafeiih/vogel/worker"
)

// startTestPool mirrors worker/worker_integration_test.go.
func startTestPool(t *testing.T) *pgxpool.Pool {
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
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

type traceArgs struct{}

func (traceArgs) Kind() string { return "trace_probe" }

type seen struct {
	span      trace.SpanContext
	requestID string
}

type traceWorker struct {
	river.WorkerDefaults[traceArgs]
	seen chan seen
}

func (w *traceWorker) Work(ctx context.Context, _ *river.Job[traceArgs]) error {
	w.seen <- seen{span: trace.SpanContextFromContext(ctx), requestID: reqctx.RequestIDFromContext(ctx)}
	return nil
}

// Through worker.WithMiddleware, on an insert-only queue and a working one,
// the job is worked inside the trace of the request that enqueued it, and
// the request ID still travels next to the traceparent.
func TestMiddleware_ThroughTheQueue(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	mw := worker.WithMiddleware(otelriver.NewMiddleware(otelriver.WithTracerProvider(tp)))

	pool := startTestPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const schema = "river"
	require.NoError(t, worker.EnsureSchema(ctx, pool, schema))
	cfg := worker.Config{Schema: schema, DefaultMaxWorkers: 1}

	api, err := worker.NewRiverQueue(pool, nil, cfg, logger, mw)
	require.NoError(t, err)
	require.NoError(t, api.Migrate(ctx))

	w := &traceWorker{seen: make(chan seen, 1)}
	workers := river.NewWorkers()
	river.AddWorker(workers, w)
	consumer, err := worker.NewRiverQueue(pool, workers, cfg, logger, mw)
	require.NoError(t, err)

	reqCtx, req := tp.Tracer("test").Start(reqctx.WithRequestID(ctx, "req-otel"), "POST /orders")
	_, err = api.Enqueue(reqCtx, traceArgs{}, nil)
	require.NoError(t, err)
	req.End()

	var requestID, traceparent string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT metadata->>'vogel_request_id', metadata->>'vogel_traceparent' FROM river.river_job WHERE kind = 'trace_probe'`,
	).Scan(&requestID, &traceparent))
	assert.Equal(t, "req-otel", requestID)
	assert.Contains(t, traceparent, req.SpanContext().TraceID().String())

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	require.NoError(t, consumer.Start(runCtx))
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = consumer.Stop(stopCtx)
	})

	select {
	case got := <-w.seen:
		assert.Equal(t, req.SpanContext().TraceID(), got.span.TraceID(), "the job is worked in the request's trace")
		assert.Equal(t, "req-otel", got.requestID)
	case <-runCtx.Done():
		t.Fatal("job not worked in time")
	}
}
