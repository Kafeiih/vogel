//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/workflow"
	"github.com/kafeiih/vogel/workflow/migrations"
	"github.com/kafeiih/vogel/workflow/postgres"
)

// The append-only guard raises SQLSTATE 23001 (restrict_violation) with a
// message that starts with this stable prefix; see
// 005_workflow_event_append_only.sql. Deleting a case that still has events
// fails on the ON DELETE RESTRICT foreign key with 23503.
const (
	appendOnlySQLState  = "23001"
	appendOnlyMsgPrefix = "workflow_event is append-only"
	fkViolationSQLState = "23503"
)

// openCaseWithEvents opens a case through the engine and records one failure
// on it, so it has two events (opened, failed) and still rests on its start
// node, and returns it.
func openCaseWithEvents(t *testing.T, pool *pgxpool.Pool, externalID string) *workflow.Case {
	t.Helper()
	ctx := context.Background()
	e := tenantTestEngine(t, postgres.NewRepository())
	human := workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}
	c, err := e.Open(ctx, pool, workflow.OpenInput{Definition: "purchase", Domain: "compras", ExternalID: externalID, Unit: "finance", Actor: human})
	require.NoError(t, err)
	_, err = e.RecordFailure(ctx, pool, workflow.FailureInput{CaseID: c.ID, Actor: human, Action: "submit", Kind: workflow.FailureGuardRejected})
	require.NoError(t, err)
	return c
}

func requireAppendOnlyRejection(t *testing.T, err error, op string) {
	t.Helper()
	require.Error(t, err, "%s on workflow_event must be rejected", op)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "expected a *pgconn.PgError, got %T: %v", err, err)
	assert.Equal(t, appendOnlySQLState, pgErr.Code)
	assert.True(t, strings.HasPrefix(pgErr.Message, appendOnlyMsgPrefix), "message %q must start with %q", pgErr.Message, appendOnlyMsgPrefix)
	assert.Contains(t, pgErr.Message, op, "message must name the rejected operation")
}

func countEvents(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM workflow_event`).Scan(&n))
	return n
}

func TestWorkflowEvent_UpdateIsRejected(t *testing.T) {
	pool := startTestPool(t)
	c := openCaseWithEvents(t, pool, "ao-update")

	_, err := pool.Exec(context.Background(), `UPDATE workflow_event SET actor_id = 'mallory' WHERE case_id = $1`, c.ID)
	requireAppendOnlyRejection(t, err, "UPDATE")
	assert.Equal(t, 2, countEvents(t, pool))
}

func TestWorkflowEvent_DeleteIsRejected(t *testing.T) {
	pool := startTestPool(t)
	c := openCaseWithEvents(t, pool, "ao-delete")

	_, err := pool.Exec(context.Background(), `DELETE FROM workflow_event WHERE case_id = $1`, c.ID)
	requireAppendOnlyRejection(t, err, "DELETE")
	assert.Equal(t, 2, countEvents(t, pool))
}

func TestWorkflowEvent_TruncateIsRejected(t *testing.T) {
	pool := startTestPool(t)
	openCaseWithEvents(t, pool, "ao-truncate")

	_, err := pool.Exec(context.Background(), `TRUNCATE workflow_event`)
	requireAppendOnlyRejection(t, err, "TRUNCATE")
	assert.Equal(t, 2, countEvents(t, pool))
}

func TestWorkflowCase_DeleteWithEventsIsRestricted(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()
	c := openCaseWithEvents(t, pool, "ao-case")

	_, err := pool.Exec(ctx, `DELETE FROM workflow_case WHERE id = $1`, c.ID)
	require.Error(t, err, "a case with events must not be deletable")
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "got %T: %v", err, err)
	assert.Equal(t, fkViolationSQLState, pgErr.Code)
	assert.Equal(t, "workflow_event_case_id_fkey", pgErr.ConstraintName)
	assert.Equal(t, 2, countEvents(t, pool))

	// A case that never got an event can still be removed.
	bare := newCase("compras", "ao-bare")
	require.NoError(t, postgres.NewRepository().Create(ctx, pool, bare))
	_, err = pool.Exec(ctx, `DELETE FROM workflow_case WHERE id = $1`, bare.ID)
	require.NoError(t, err)
}

func TestWorkflowEvent_AppendStillWorksAfterRejections(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()
	c := openCaseWithEvents(t, pool, "ao-append")

	_, err := pool.Exec(ctx, `DELETE FROM workflow_event WHERE case_id = $1`, c.ID)
	requireAppendOnlyRejection(t, err, "DELETE")

	e := tenantTestEngine(t, postgres.NewRepository())
	_, err = e.Move(ctx, pool, workflow.MoveInput{CaseID: c.ID, Action: "submit", Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}})
	require.NoError(t, err, "the guard must never block an append")
	assert.Equal(t, 4, countEvents(t, pool), "opened, failed, moved, closed")
}

func TestMigration005_UpDownUpRoundTrip(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	opts := silentOptions()
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), opts))

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	c := openCaseWithEvents(t, pool, "ao-roundtrip")

	// Down reverts only 005: the guard goes away and CASCADE is back.
	require.NoError(t, migrate.Down(ctx, connStr, migrations.FS(), opts))

	var objects int
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT (SELECT count(*) FROM pg_trigger WHERE tgrelid = 'workflow_event'::regclass AND NOT tgisinternal)
             + (SELECT count(*) FROM pg_proc WHERE proname = 'vogel_workflow_event_reject_mutation')`).Scan(&objects))
	assert.Zero(t, objects, "Down must drop both triggers and the function")

	var deleteRule string
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT confdeltype::text FROM pg_constraint WHERE conname = 'workflow_event_case_id_fkey'`).Scan(&deleteRule))
	assert.Equal(t, "c", deleteRule, "Down must restore ON DELETE CASCADE")
	assert.Equal(t, 2, countEvents(t, pool), "Down leaves the rows untouched")

	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), opts), "Up must apply again after Down")
	_, err = pool.Exec(ctx, `DELETE FROM workflow_event WHERE case_id = $1`, c.ID)
	requireAppendOnlyRejection(t, err, "DELETE")
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT confdeltype::text FROM pg_constraint WHERE conname = 'workflow_event_case_id_fkey'`).Scan(&deleteRule))
	assert.Equal(t, "r", deleteRule, "Up must set ON DELETE RESTRICT")

	// The key is added NOT VALID (no blocking scan) yet still enforced, and an
	// operator can validate it afterwards.
	_, err = pool.Exec(ctx, `ALTER TABLE workflow_event VALIDATE CONSTRAINT workflow_event_case_id_fkey`)
	require.NoError(t, err, "existing rows already satisfy the key")
}
