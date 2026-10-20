//go:build integration

package action_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/worker"
	"github.com/kafeiih/vogel/workflow"
	"github.com/kafeiih/vogel/workflow/migrations"
	"github.com/kafeiih/vogel/workflow/postgres"
)

// startTestPostgres mirrors worker/worker_integration_test.go.
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

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const riverSchema = "river"

// setup applies workflow and River migrations and returns the pool and an
// insert-only queue.
func setup(t *testing.T) (*pgxpool.Pool, *worker.RiverQueue) {
	t.Helper()
	ctx := context.Background()
	connStr := startTestPostgres(t)
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), migrate.Options{
		Logger: silentLogger(), TableName: migrations.DefaultTableName,
	}))

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	require.NoError(t, worker.EnsureSchema(ctx, pool, riverSchema))
	q, err := worker.NewRiverQueue(pool, nil, worker.Config{Schema: riverSchema, DefaultMaxWorkers: 2}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, q.Migrate(ctx))
	return pool, q
}

type jobRow struct {
	Seq    int64
	Effect string
}

func listJobs(t *testing.T, pool *pgxpool.Pool) []jobRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT (args->>'seq')::bigint, args->>'effect' FROM river.river_job WHERE kind = $1 ORDER BY id`, action.Kind)
	require.NoError(t, err)
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (jobRow, error) {
		var j jobRow
		err := r.Scan(&j.Seq, &j.Effect)
		return j, err
	})
	require.NoError(t, err)
	return out
}

func integrationDefinition() workflow.Definition {
	d := effectsDefinition()
	d.Nodes[0].Effects = nil // nothing on open, so only Move enqueues
	return d
}

func newEngine(t *testing.T, r *action.Registry) *workflow.Engine {
	t.Helper()
	e := workflow.New(postgres.NewRepository(), workflow.Config{}, silentLogger(), workflow.WithEffectSink(r))
	require.NoError(t, e.RegisterGuard("is_big", func(context.Context, pgxtx.DBTX, workflow.Case) (bool, error) { return true, nil }))
	require.NoError(t, e.Register(integrationDefinition()))
	return e
}

func registerAll(t *testing.T, r *action.Registry, h action.Handler) {
	t.Helper()
	for _, n := range integrationDefinition().EffectNames() {
		require.NoError(t, r.Register(n, h))
	}
}

func openCase(t *testing.T, pool *pgxpool.Pool, e *workflow.Engine) *workflow.Case {
	t.Helper()
	c, err := e.Open(context.Background(), pool, workflow.OpenInput{
		Definition: "purchase", Domain: "compras", ExternalID: "solicitud-1", Unit: "finanzas", ActorID: "u1",
	})
	require.NoError(t, err)
	return c
}

var errRollback = errors.New("rollback on purpose")

func TestIntegration_MoveRolledBack_EnqueuesNothing(t *testing.T) {
	pool, q := setup(t)
	r := action.NewRegistry(q)
	registerAll(t, r, nopHandler())
	e := newEngine(t, r)
	c := openCase(t, pool, e)

	err := pgxtx.NewPgxTxManager(pool).WithTx(context.Background(), func(txCtx context.Context) error {
		_, err := e.Move(txCtx, pgxtx.TxFromContext(txCtx), workflow.MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u2"})
		require.NoError(t, err)
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)

	assert.Empty(t, listJobs(t, pool), "a rollback must enqueue nothing")
}

func TestIntegration_MoveCommitted_EnqueuesExactlyOnceAndWorkerRuns(t *testing.T) {
	pool, q := setup(t)
	r := action.NewRegistry(q)

	var mu sync.Mutex
	ran := map[string]action.Run{}
	done := make(chan struct{}, 8)
	registerAll(t, r, action.HandlerFunc(func(_ context.Context, run action.Run) error {
		mu.Lock()
		ran[run.Effect] = run
		mu.Unlock()
		done <- struct{}{}
		return nil
	}))
	e := newEngine(t, r)
	c := openCase(t, pool, e)

	ctx := context.Background()
	tm := pgxtx.NewPgxTxManager(pool)
	var events []workflow.Event
	var moved *workflow.Case
	require.NoError(t, tm.WithTx(ctx, func(txCtx context.Context) error {
		var err error
		moved, err = e.Move(txCtx, pgxtx.TxFromContext(txCtx), workflow.MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u2"})
		return err
	}))
	evs, err := postgres.NewRepository().ListEvents(ctx, pool, c.ID)
	require.NoError(t, err)
	events = evs[1:] // everything Move appended (Seq 1 is EventOpened)

	want := []jobRow{{2, "on_submit"}, {2, "audit"}, {3, "on_approved"}, {3, "on_big"}}
	assert.Equal(t, want, listJobs(t, pool))

	// Re-dispatching the same events is a no-op.
	require.NoError(t, tm.WithTx(ctx, func(txCtx context.Context) error {
		return r.Dispatch(txCtx, pgxtx.TxFromContext(txCtx), integrationDefinition(), *moved, events)
	}))
	assert.Equal(t, want, listJobs(t, pool), "re-dispatch must not duplicate jobs")

	// A transaction on ctx alone is not enough: the engine wrote through db,
	// so enqueueing on another handle could commit apart from the case.
	err = tm.WithTx(ctx, func(txCtx context.Context) error {
		return r.Dispatch(txCtx, pool, integrationDefinition(), *moved, events)
	})
	require.ErrorIs(t, err, pgxtx.ErrNoTx)

	workers := river.NewWorkers()
	require.NoError(t, action.AddWorker(workers, action.NewWorker(r)))
	consumer, err := worker.NewRiverQueue(pool, workers, worker.Config{Schema: riverSchema, DefaultMaxWorkers: 2}, silentLogger())
	require.NoError(t, err)
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	require.NoError(t, consumer.Start(runCtx))
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = consumer.Stop(stopCtx)
	})

	for i := 0; i < len(want); i++ {
		select {
		case <-done:
		case <-runCtx.Done():
			t.Fatalf("effects not run in time; ran so far: %v", ran)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, ran, len(want))
	got := ran["on_big"]
	assert.Equal(t, c.ID, got.CaseID)
	assert.Equal(t, int64(3), got.Seq)
	assert.Equal(t, "solicitud-1", got.ExternalID)
	assert.Equal(t, "u2", got.ActorID)
	assert.Equal(t, 1, got.Attempt)
}
