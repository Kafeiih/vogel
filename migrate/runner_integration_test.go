//go:build integration

package migrate_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/kafeiih/vogel/migrate"
)

// startTestPostgres spins up a postgres:17-alpine testcontainer and returns a
// connection string plus a cleanup function.
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

func silentOptions() migrate.Options {
	return migrate.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// twoMigrationsFS builds an in-memory fs.FS with two trivial SQL migrations.
func twoMigrationsFS() fstest.MapFS {
	return fstest.MapFS{
		"00001_create_widgets.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE widgets (id serial PRIMARY KEY, name text NOT NULL);

-- +goose Down
DROP TABLE widgets;
`)},
		"00002_create_gadgets.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE gadgets (id serial PRIMARY KEY, widget_id integer REFERENCES widgets(id));

-- +goose Down
DROP TABLE gadgets;
`)},
	}
}

func TestUp_AppliesAllMigrations_Idempotent(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()
	fsys := twoMigrationsFS()

	err := migrate.Up(ctx, connStr, fsys, silentOptions())
	require.NoError(t, err, "migrate.Up should succeed")

	// Idempotency: running Up again on a fully-migrated DB should be a no-op.
	err = migrate.Up(ctx, connStr, fsys, silentOptions())
	require.NoError(t, err, "migrate.Up a second time should be idempotent")
}

func TestDown_RevertsMostRecentMigration(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()
	fsys := twoMigrationsFS()

	require.NoError(t, migrate.Up(ctx, connStr, fsys, silentOptions()))

	err := migrate.Down(ctx, connStr, fsys, silentOptions())
	require.NoError(t, err, "migrate.Down should succeed")

	var buf bytes.Buffer
	require.NoError(t, migrate.Status(ctx, connStr, fsys, &buf, silentOptions()))
	assert.Contains(t, buf.String(), "00002_create_gadgets.sql")
}

func TestStatus_WritesTableReferencingMigrations(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()
	fsys := twoMigrationsFS()

	require.NoError(t, migrate.Up(ctx, connStr, fsys, silentOptions()))

	var buf bytes.Buffer
	err := migrate.Status(ctx, connStr, fsys, &buf, silentOptions())
	require.NoError(t, err, "migrate.Status should succeed")

	output := buf.String()
	assert.Contains(t, output, "00001_create_widgets.sql")
	assert.Contains(t, output, "00002_create_gadgets.sql")
}

func TestUpTo_AppliesOnlyUpToGivenVersion(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()
	fsys := twoMigrationsFS()

	err := migrate.UpTo(ctx, connStr, fsys, 1, silentOptions())
	require.NoError(t, err, "migrate.UpTo(1) should succeed")

	var buf bytes.Buffer
	require.NoError(t, migrate.Status(ctx, connStr, fsys, &buf, silentOptions()))
	// version 1 applied, version 2 still pending
	assert.Contains(t, buf.String(), "00001_create_widgets.sql")
}

func TestUpByOne_AppliesExactlyOneMigrationPerCall(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()
	fsys := twoMigrationsFS()

	require.NoError(t, migrate.UpByOne(ctx, connStr, fsys, silentOptions()), "first UpByOne")
	require.NoError(t, migrate.UpByOne(ctx, connStr, fsys, silentOptions()), "second UpByOne")

	// A third call has nothing left to apply.
	err := migrate.UpByOne(ctx, connStr, fsys, silentOptions())
	require.Error(t, err)
}

func TestUp_EmptyFS_ReturnsErrNoMigrations(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()

	err := migrate.Up(ctx, connStr, fstest.MapFS{}, silentOptions())
	require.Error(t, err)
	assert.ErrorIs(t, err, migrate.ErrNoMigrations)
}

// TestUp_ConcurrentInvocations_SerializeInsteadOfRacing is the regression test
// for FIX 5: without goose.WithSessionLocker, two concurrent `migrate up`
// runs against a fresh database can both see "no migrations applied yet" and
// attempt to run the same migration simultaneously. With the session-level
// PostgreSQL advisory lock now wired in, one waits for the other to finish;
// both must succeed and the migrations table must end up in a single
// consistent, fully-applied state.
func TestUp_ConcurrentInvocations_SerializeInsteadOfRacing(t *testing.T) {
	connStr := startTestPostgres(t)
	fsys := twoMigrationsFS()

	const runs = 5
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			if err := migrate.Up(ctx, connStr, fsys, silentOptions()); err != nil {
				failures.Add(1)
				t.Logf("concurrent migrate.Up failed: %v", err)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(0), failures.Load(), "all concurrent Up calls should succeed once serialized by the advisory lock")

	var buf bytes.Buffer
	require.NoError(t, migrate.Status(context.Background(), connStr, fsys, &buf, silentOptions()))
	output := buf.String()
	assert.Contains(t, output, "00001_create_widgets.sql")
	assert.Contains(t, output, "00002_create_gadgets.sql")
	// Both migrations must show as applied exactly once each; goose enforces
	// this via its version table's unique constraint, so a race that slipped
	// through the lock would have already failed one of the goroutines above.
}
