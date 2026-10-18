//go:build integration

package worker_test

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

	"github.com/kafeiih/vogel/reqctx"
	"github.com/kafeiih/vogel/worker"
)

// startTestPostgres spins up a postgres:17-alpine testcontainer and returns
// its connection string. Mirrors postgres/postgres_integration_test.go and
// migrate/runner_integration_test.go so all three packages provision their
// container the same way.
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

func startTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	connStr := startTestPostgres(t)

	pool, err := pgxpool.New(context.Background(), connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEnsureSchema_Idempotent(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()

	require.NoError(t, worker.EnsureSchema(ctx, pool, "river"), "first EnsureSchema call")
	require.NoError(t, worker.EnsureSchema(ctx, pool, "river"), "second EnsureSchema call must be a no-op")

	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)`, "river",
	).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists, "expected schema %q to exist", "river")
}

func TestEnsureSchema_RejectsInvalidName_WithoutTouchingDatabase(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()

	err := worker.EnsureSchema(ctx, pool, "river; DROP TABLE x")
	require.Error(t, err)

	var exists bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)`, "river",
	).Scan(&exists)
	require.NoError(t, err)
	assert.False(t, exists, "invalid schema name must not have created anything")
}

// TestMigrate_RunsRiverMigrationsAgainstEnsuredSchema proves the full
// startup sequence a worker binary follows: EnsureSchema creates the schema,
// then Migrate runs River's own migrations inside it, leaving River's tables
// present under that schema.
func TestMigrate_RunsRiverMigrationsAgainstEnsuredSchema(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()

	const schema = "river"
	require.NoError(t, worker.EnsureSchema(ctx, pool, schema))

	cfg := worker.Config{Schema: schema, DefaultMaxWorkers: 100}
	q, err := worker.NewRiverQueue(pool, nil, cfg, silentLogger())
	require.NoError(t, err)

	require.NoError(t, q.Migrate(ctx), "river migrations should apply cleanly")

	var exists bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'river_job')`, schema,
	).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists, "expected river_job table under schema %q after Migrate", schema)
}

// captureArgs is a job whose worker reports the request ID its context
// carried, keyed by the job's Tag.
type captureArgs struct {
	Tag string `json:"tag"`
}

func (captureArgs) Kind() string { return "capture_request_id" }

type captureWorker struct {
	river.WorkerDefaults[captureArgs]
	seen chan [2]string
}

func (w *captureWorker) Work(ctx context.Context, job *river.Job[captureArgs]) error {
	w.seen <- [2]string{job.Args.Tag, reqctx.RequestIDFromContext(ctx)}
	return nil
}

// TestRequestID_TravelsFromEnqueueToWork runs the two roles a deployment
// has, an insert-only API queue and a separate worker queue, against one
// database: the request ID of the enqueuing context must be stored in the
// job's metadata and come back in the context the worker's Work receives.
// A job enqueued outside a request is worked with no request ID.
func TestRequestID_TravelsFromEnqueueToWork(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()

	const schema = "river"
	require.NoError(t, worker.EnsureSchema(ctx, pool, schema))
	cfg := worker.Config{Schema: schema, DefaultMaxWorkers: 2}

	api, err := worker.NewRiverQueue(pool, nil, cfg, silentLogger())
	require.NoError(t, err)
	require.NoError(t, api.Migrate(ctx))

	w := &captureWorker{seen: make(chan [2]string, 2)}
	workers := river.NewWorkers()
	river.AddWorker(workers, w)
	consumer, err := worker.NewRiverQueue(pool, workers, cfg, silentLogger())
	require.NoError(t, err)

	reqCtx := reqctx.WithRequestID(ctx, "req-e2e")
	_, err = api.Enqueue(reqCtx, captureArgs{Tag: "in-request"}, nil)
	require.NoError(t, err)
	_, err = api.Enqueue(ctx, captureArgs{Tag: "no-request"}, nil)
	require.NoError(t, err)

	var stored string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT metadata->>'vogel_request_id' FROM river.river_job WHERE args->>'tag' = 'in-request'`,
	).Scan(&stored))
	assert.Equal(t, "req-e2e", stored, "request ID must be persisted in the job metadata")

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	require.NoError(t, consumer.Start(runCtx))
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = consumer.Stop(stopCtx)
	})

	got := map[string]string{}
	for len(got) < 2 {
		select {
		case s := <-w.seen:
			got[s[0]] = s[1]
		case <-runCtx.Done():
			t.Fatalf("jobs not worked in time; seen so far: %v", got)
		}
	}
	assert.Equal(t, "req-e2e", got["in-request"])
	assert.Equal(t, "", got["no-request"])
}
