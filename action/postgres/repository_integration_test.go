//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/action/migrations"
	"github.com/kafeiih/vogel/action/postgres"
	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/worker"
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

func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	connStr := startTestPostgres(t)
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), migrate.Options{
		Logger: silentLogger(), TableName: migrations.DefaultTableName,
	}))
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// nopQueue satisfies worker.Queue for a Registry whose Handlers never fan out.
type nopQueue struct{ worker.Queue }

func newWorker(t *testing.T, pool *pgxpool.Pool, handlers map[string]action.Handler) *action.Worker {
	t.Helper()
	r := action.NewRegistry(nopQueue{})
	for name, h := range handlers {
		require.NoError(t, r.Register(name, h))
	}
	return action.NewWorker(r, action.WithAttemptLog(pool), action.WithLogger(silentLogger()),
		action.WithMetricsRegisterer(prometheus.NewRegistry()))
}

func jobRow(id int64, attempt int, args action.Args) *river.Job[action.Args] {
	return &river.Job[action.Args]{JobRow: &rivertype.JobRow{ID: id, Attempt: attempt, MaxAttempts: 25}, Args: args}
}

func ok() action.Handler {
	return action.HandlerFunc(func(context.Context, action.Run) error { return nil })
}

func TestAttemptLog_Integration_OneRowPerAttemptAndDuplicateIsNoop(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	fails := true
	w := newWorker(t, pool, map[string]action.Handler{
		"notify": action.HandlerFunc(func(context.Context, action.Run) error {
			if fails {
				return errors.New("smtp down")
			}
			return nil
		}),
	})
	args := action.Args{CaseID: uuid.New(), Seq: 2, Effect: "notify", ActorID: "u1"}

	require.Error(t, w.Work(ctx, jobRow(7, 1, args)))
	require.Error(t, w.Work(ctx, jobRow(7, 1, args)), "the same attempt again, as after a worker crash")
	fails = false
	require.NoError(t, w.Work(ctx, jobRow(7, 2, args)))

	got, err := postgres.NewAttemptRepository().ListByEvent(ctx, pool, args.CaseID, 2)
	require.NoError(t, err)
	require.Len(t, got, 2, "one row per attempt; the duplicate write of attempt 1 is a no-op")
	assert.Equal(t, 1, got[0].Attempt)
	assert.Equal(t, action.OutcomeRetry, got[0].Outcome)
	assert.Equal(t, "*errors.errorString", got[0].ErrorClass)
	assert.Equal(t, 2, got[1].Attempt)
	assert.Equal(t, action.OutcomeSucceeded, got[1].Outcome)
	assert.Equal(t, int64(7), got[1].JobID)
	assert.Equal(t, "u1", got[1].ActorID)
	assert.False(t, got[1].FinishedAt.Before(got[1].StartedAt))
}

func TestAttemptRepository_ListByCaseOrdersBySeqThenAttempt(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	w := newWorker(t, pool, map[string]action.Handler{"notify": ok()})
	caseID := uuid.New()
	other := uuid.New()

	require.NoError(t, w.Work(ctx, jobRow(30, 2, action.Args{CaseID: caseID, Seq: 5, Effect: "notify"})))
	require.NoError(t, w.Work(ctx, jobRow(20, 1, action.Args{CaseID: caseID, Seq: 9, Effect: "notify"})))
	require.NoError(t, w.Work(ctx, jobRow(30, 1, action.Args{CaseID: caseID, Seq: 5, Effect: "notify"})))
	require.NoError(t, w.Work(ctx, jobRow(40, 1, action.Args{CaseID: other, Seq: 1, Effect: "notify"})))

	got, err := postgres.NewAttemptRepository().ListByCase(ctx, pool, caseID)
	require.NoError(t, err)
	require.Len(t, got, 3, "only the case's own attempts")
	assert.Equal(t, []int64{5, 5, 9}, []int64{got[0].Seq, got[1].Seq, got[2].Seq})
	assert.Equal(t, []int{1, 2, 1}, []int{got[0].Attempt, got[1].Attempt, got[2].Attempt})

	byEvent, err := postgres.NewAttemptRepository().ListByEvent(ctx, pool, caseID, 9)
	require.NoError(t, err)
	require.Len(t, byEvent, 1)
	assert.Equal(t, int64(20), byEvent[0].JobID)
}

func TestActionAttempt_UpdateIsRejected(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	w := newWorker(t, pool, map[string]action.Handler{"notify": ok()})
	caseID := uuid.New()
	require.NoError(t, w.Work(ctx, jobRow(1, 1, action.Args{CaseID: caseID, Seq: 1, Effect: "notify"})))

	for _, stmt := range []string{
		`UPDATE action_attempt SET outcome = 'retry' WHERE case_id = $1`,
		`DELETE FROM action_attempt WHERE case_id = $1`,
	} {
		_, err := pool.Exec(ctx, stmt, caseID)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, stmt)
		assert.Equal(t, "23001", pgErr.Code, stmt)
		assert.Contains(t, pgErr.Message, "action_attempt is append-only", stmt)
	}
	_, err := pool.Exec(ctx, `TRUNCATE action_attempt`)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "23001", pgErr.Code)

	got, err := postgres.NewAttemptRepository().ListByCase(ctx, pool, caseID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, action.OutcomeSucceeded, got[0].Outcome)
}
