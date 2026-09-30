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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/audit/migrations"
	auditpg "github.com/kafeiih/vogel/audit/postgres"
	"github.com/kafeiih/vogel/migrate"
)

// The append-only guard raises SQLSTATE 23001 (restrict_violation) with a
// message that starts with this stable prefix; see 002_audit_log_append_only.sql.
const (
	appendOnlySQLState  = "23001"
	appendOnlyMsgPrefix = "audit_log is append-only"
)

func appendOnlyMigrateOptions() migrate.Options {
	return migrate.Options{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		TableName: migrations.DefaultTableName,
	}
}

func newAppendOnlyPool(t *testing.T, connStr string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// insertViaRecorder writes one row through the real Recorder + Repository and
// returns its id.
func insertViaRecorder(t *testing.T, pool *pgxpool.Pool, resourceID string) string {
	t.Helper()
	recorder := audit.NewRecorder(auditpg.NewRepository(pool))
	require.NoError(t, recorder.Record(context.Background(), pool,
		audit.SourceWorker, audit.ActionCreate, "widget", resourceID,
		audit.WithAfterSnapshot(map[string]any{"name": "gizmo"}),
	))

	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT id::text FROM audit_log WHERE resource_id = $1`, resourceID).Scan(&id))
	return id
}

func requireAppendOnlyRejection(t *testing.T, err error, op string) {
	t.Helper()
	require.Error(t, err, "%s on audit_log must be rejected", op)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "expected a *pgconn.PgError, got %T: %v", err, err)
	assert.Equal(t, appendOnlySQLState, pgErr.Code)
	assert.Contains(t, pgErr.Message, appendOnlyMsgPrefix)
	assert.Contains(t, pgErr.Message, op, "message must name the rejected operation")
}

func TestAuditLog_AppendOnly_InsertWorks_MutationsAreRejected(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	id := insertViaRecorder(t, pool, "w-1")

	got, err := repo.GetByID(ctx, uuid.MustParse(id))
	require.NoError(t, err)
	assert.Equal(t, "w-1", got.ResourceID)

	t.Run("UPDATE", func(t *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE audit_log SET username = 'mallory' WHERE id = $1`, id)
		requireAppendOnlyRejection(t, err, "UPDATE")
	})
	t.Run("DELETE", func(t *testing.T) {
		_, err := pool.Exec(ctx, `DELETE FROM audit_log WHERE id = $1`, id)
		requireAppendOnlyRejection(t, err, "DELETE")
	})
	t.Run("TRUNCATE", func(t *testing.T) {
		_, err := pool.Exec(ctx, `TRUNCATE audit_log`)
		requireAppendOnlyRejection(t, err, "TRUNCATE")
	})

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n))
	assert.Equal(t, 1, n, "rejected mutations must leave the row in place")

	// Appending still works after the rejections.
	insertViaRecorder(t, pool, "w-2")
}

func TestAuditLog_AppendOnly_UpDownUpRoundTrip(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()
	opts := appendOnlyMigrateOptions()

	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), opts))
	pool := newAppendOnlyPool(t, connStr)
	id := insertViaRecorder(t, pool, "w-1")

	_, err := pool.Exec(ctx, `UPDATE audit_log SET username = 'x' WHERE id = $1`, id)
	requireAppendOnlyRejection(t, err, "UPDATE")

	// Down reverts only 002: the guard goes away, the table and its rows stay.
	require.NoError(t, migrate.Down(ctx, connStr, migrations.FS(), opts))

	var objects int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM pg_trigger WHERE tgrelid = 'audit_log'::regclass AND NOT tgisinternal)
		     + (SELECT count(*) FROM pg_proc WHERE proname = 'vogel_audit_log_reject_mutation')`).Scan(&objects))
	assert.Zero(t, objects, "Down must drop both triggers and the function")

	tag, err := pool.Exec(ctx, `UPDATE audit_log SET username = 'x' WHERE id = $1`, id)
	require.NoError(t, err, "UPDATE must work again after Down")
	assert.EqualValues(t, 1, tag.RowsAffected())
	tag, err = pool.Exec(ctx, `DELETE FROM audit_log WHERE id = $1`, id)
	require.NoError(t, err, "DELETE must work again after Down")
	assert.EqualValues(t, 1, tag.RowsAffected())
	_, err = pool.Exec(ctx, `TRUNCATE audit_log`)
	require.NoError(t, err, "TRUNCATE must work again after Down")

	// Up again re-arms the guard.
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), opts))
	id = insertViaRecorder(t, pool, "w-2")
	_, err = pool.Exec(ctx, `DELETE FROM audit_log WHERE id = $1`, id)
	requireAppendOnlyRejection(t, err, "DELETE")
}

func TestAuditLog_AppendOnly_AppliesOverExistingTableWithRows(t *testing.T) {
	connStr := startTestPostgres(t)
	ctx := context.Background()
	opts := appendOnlyMigrateOptions()

	// An existing consumer: only 001 applied, with history already recorded.
	require.NoError(t, migrate.UpTo(ctx, connStr, migrations.FS(), 1, opts))
	pool := newAppendOnlyPool(t, connStr)
	for _, rid := range []string{"w-1", "w-2", "w-3"} {
		_, err := pool.Exec(ctx, `
			INSERT INTO audit_log (id, resource_type, resource_id, operation_category, actor_id, username, created_at)
			VALUES ($1, 'widget', $2, 'create', 'u-1', 'alice', $3)`,
			uuid.New(), rid, time.Now().UTC())
		require.NoError(t, err)
	}

	// The upgrade: 002 lands on top without touching the rows.
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), opts))

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n))
	assert.Equal(t, 3, n, "pre-existing rows survive the upgrade")

	_, err := pool.Exec(ctx, `UPDATE audit_log SET username = 'mallory'`)
	requireAppendOnlyRejection(t, err, "UPDATE")
	_, err = pool.Exec(ctx, `DELETE FROM audit_log`)
	requireAppendOnlyRejection(t, err, "DELETE")
	_, err = pool.Exec(ctx, `TRUNCATE audit_log`)
	requireAppendOnlyRejection(t, err, "TRUNCATE")
}

// TestAuditLog_AppendOnly_DocumentedRetentionProcedureWorks pins the purge
// recipe in the 002 header comment: disable both triggers, delete, re-enable,
// all in one transaction, leaving the table guarded again afterwards.
func TestAuditLog_AppendOnly_DocumentedRetentionProcedureWorks(t *testing.T) {
	_, pool := newTestRepository(t)
	ctx := context.Background()

	id := insertViaRecorder(t, pool, "w-1")

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	for _, stmt := range []string{
		`ALTER TABLE audit_log DISABLE TRIGGER vogel_audit_log_no_update_delete`,
		`ALTER TABLE audit_log DISABLE TRIGGER vogel_audit_log_no_truncate`,
		`DELETE FROM audit_log WHERE created_at < now() + interval '1 day'`,
		`ALTER TABLE audit_log ENABLE TRIGGER vogel_audit_log_no_update_delete`,
		`ALTER TABLE audit_log ENABLE TRIGGER vogel_audit_log_no_truncate`,
	} {
		_, err := tx.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	require.NoError(t, tx.Commit(ctx))

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE id = $1`, id).Scan(&n))
	assert.Zero(t, n, "the purge removed the row")

	id = insertViaRecorder(t, pool, "w-2")
	_, err = pool.Exec(ctx, `DELETE FROM audit_log WHERE id = $1`, id)
	requireAppendOnlyRejection(t, err, "DELETE")
	_, err = pool.Exec(ctx, `TRUNCATE audit_log`)
	requireAppendOnlyRejection(t, err, "TRUNCATE")
}
