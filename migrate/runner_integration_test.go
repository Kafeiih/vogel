//go:build integration

package migrate_test

import (
	"bytes"
	"context"
	"database/sql"
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

	// registers the pgx stdlib driver under the "pgx" name, used here only to
	// open a plain *sql.DB for asserting on the resulting schema directly.
	_ "github.com/jackc/pgx/v5/stdlib"

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

// TestUp_IndependentTableNames_DoNotCollide is the regression test for FIX 4:
// a library-owned migration set (e.g. vogel/audit/migrations) and an
// application's own migration set both start numbering at 001. Without
// separate goose version tables, the second set to run would see version 1
// already marked applied and silently skip its own first migration. Using
// Options.TableName to give the library set its own version table ("
// vogel_db_version") must let both sets apply fully and independently
// against the very same database.
func TestUp_IndependentTableNames_DoNotCollide(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()

	appFS := twoMigrationsFS() // 00001_create_widgets.sql, 00002_create_gadgets.sql

	// A stand-in "library" migration set that deliberately reuses version 1,
	// exactly as vogel/audit/migrations' 001_create_audit_log.sql does
	// relative to any consuming application's own 001.
	libFS := fstest.MapFS{
		"00001_create_lib_widgets.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE lib_widgets (id serial PRIMARY KEY, name text NOT NULL);

-- +goose Down
DROP TABLE lib_widgets;
`)},
	}

	// Application migrations track in goose's default table.
	require.NoError(t, migrate.Up(ctx, connStr, appFS, silentOptions()), "application migrate.Up")

	// Library migrations track in their own table, despite reusing version "1".
	libOpts := silentOptions()
	libOpts.TableName = "vogel_db_version"
	require.NoError(t, migrate.Up(ctx, connStr, libFS, libOpts), "library migrate.Up")

	// Both sets must report as fully applied, independently.
	var appBuf, libBuf bytes.Buffer
	require.NoError(t, migrate.Status(ctx, connStr, appFS, &appBuf, silentOptions()))
	require.NoError(t, migrate.Status(ctx, connStr, libFS, &libBuf, libOpts))
	assert.Contains(t, appBuf.String(), "00001_create_widgets.sql")
	assert.Contains(t, appBuf.String(), "00002_create_gadgets.sql")
	assert.Contains(t, libBuf.String(), "00001_create_lib_widgets.sql")

	// Prove it's not just goose's in-memory bookkeeping: both version tables
	// and both sets of application tables must actually exist side by side.
	db, err := sql.Open("pgx", connStr)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	for _, table := range []string{"goose_db_version", "vogel_db_version", "widgets", "gadgets", "lib_widgets"} {
		var exists bool
		err := db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table,
		).Scan(&exists)
		require.NoError(t, err, "checking existence of table %q", table)
		assert.True(t, exists, "expected table %q to exist", table)
	}

	// The two version tables must have tracked their migrations independently:
	// each has exactly its own single version 1 row, not a shared/duplicated one.
	var appVersionCount, libVersionCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM goose_db_version WHERE version_id = 1`).Scan(&appVersionCount))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM vogel_db_version WHERE version_id = 1`).Scan(&libVersionCount))
	assert.Equal(t, 1, appVersionCount, "goose_db_version should record exactly one version-1 row")
	assert.Equal(t, 1, libVersionCount, "vogel_db_version should record exactly one version-1 row")
}
