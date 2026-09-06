//go:build integration

package postgres_test

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

	"github.com/kafeiih/vogel/postgres"
)

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

// TestNewPool_AppliesServerSideTimeouts is the regression test for FIX 2: the
// source repositories never set statement_timeout, lock_timeout, or
// idle_in_transaction_session_timeout, so a hung handler holding an open
// transaction (or a runaway query) had no server-side backstop. This verifies
// the configured values actually land on the session via AfterConnect.
func TestNewPool_AppliesServerSideTimeouts(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()

	cfg := postgres.Config{
		URL:                             connStr,
		MaxConns:                        2,
		MinConns:                        1,
		StatementTimeout:                7 * time.Second,
		LockTimeout:                     3 * time.Second,
		IdleInTransactionSessionTimeout: 11 * time.Second,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := postgres.NewPool(ctx, cfg, logger)
	require.NoError(t, err)
	defer pool.Close()

	var statementTimeout, lockTimeout, idleTimeout string
	require.NoError(t, pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&statementTimeout))
	require.NoError(t, pool.QueryRow(ctx, "SHOW lock_timeout").Scan(&lockTimeout))
	require.NoError(t, pool.QueryRow(ctx, "SHOW idle_in_transaction_session_timeout").Scan(&idleTimeout))

	assert.Equal(t, "7s", statementTimeout)
	assert.Equal(t, "3s", lockTimeout)
	assert.Equal(t, "11s", idleTimeout)
}

// TestNewPool_DefaultTimeoutsAppliedWhenUnset verifies the sane defaults are
// used when the caller does not set any of the three timeout fields.
func TestNewPool_DefaultTimeoutsAppliedWhenUnset(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()

	cfg := postgres.Config{URL: connStr, MaxConns: 2, MinConns: 1}
	pool, err := postgres.NewPool(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	defer pool.Close()

	var statementTimeout, lockTimeout, idleTimeout string
	require.NoError(t, pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&statementTimeout))
	require.NoError(t, pool.QueryRow(ctx, "SHOW lock_timeout").Scan(&lockTimeout))
	require.NoError(t, pool.QueryRow(ctx, "SHOW idle_in_transaction_session_timeout").Scan(&idleTimeout))

	assert.Equal(t, "30s", statementTimeout)
	assert.Equal(t, "5s", lockTimeout)
	assert.Equal(t, "1min", idleTimeout)
}

// TestNewPool_NegativeTimeoutDisablesIt verifies the documented escape hatch:
// a negative Config timeout field results in PostgreSQL's "disabled" value (0).
func TestNewPool_NegativeTimeoutDisablesIt(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()

	cfg := postgres.Config{
		URL:                             connStr,
		MaxConns:                        2,
		MinConns:                        1,
		StatementTimeout:                -1,
		LockTimeout:                     -1,
		IdleInTransactionSessionTimeout: -1,
	}
	pool, err := postgres.NewPool(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	defer pool.Close()

	var statementTimeout string
	require.NoError(t, pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&statementTimeout))
	assert.Equal(t, "0", statementTimeout)
}

// TestNewPool_UserAfterConnectRunsAfterTimeouts verifies that a
// consumer-supplied AfterConnect (e.g. go-crucible's decimal codec
// registration) still runs, chained after this package's own per-connection
// setup.
func TestNewPool_UserAfterConnectRunsAfterTimeouts(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()

	var called bool
	cfg := postgres.Config{
		URL:      connStr,
		MaxConns: 1,
		MinConns: 1,
		AfterConnect: func(_ context.Context, _ *pgx.Conn) error {
			called = true
			return nil
		},
	}

	pool, err := postgres.NewPool(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	defer pool.Close()

	require.NoError(t, pool.Ping(ctx))
	assert.True(t, called, "user-supplied AfterConnect should have run")
}
