//go:build integration

package worker_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

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
