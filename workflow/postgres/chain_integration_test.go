//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/workflow"
	"github.com/kafeiih/vogel/workflow/migrations"
	"github.com/kafeiih/vogel/workflow/postgres"
)

func requireChainBrokenAt(t *testing.T, err error, caseID uuid.UUID, seq int64) {
	t.Helper()
	require.ErrorIs(t, err, workflow.ErrChainBroken)
	var broken *workflow.ChainBrokenError
	require.True(t, errors.As(err, &broken), "expected a *workflow.ChainBrokenError, got %T: %v", err, err)
	assert.Equal(t, caseID, broken.CaseID)
	assert.Equal(t, seq, broken.Seq)
}

func TestAppendEvent_ChainsDigest(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()
	e := tenantTestEngine(t, postgres.NewRepository())

	c := openCaseWithEvents(t, pool, "chain-1")
	failed, err := e.RecordFailure(ctx, pool, workflow.FailureInput{
		CaseID: c.ID, Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u2"}, Action: "submit",
		Kind: workflow.FailureValidationFailed, Detail: []byte(`{"b": 1, "a": 1e2}`),
	})
	require.NoError(t, err)

	history, err := e.History(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, history, 3)
	assert.Nil(t, history[0].PrevDigest, "the first event starts the chain")
	for i, ev := range history {
		assert.Len(t, ev.Digest, 32, "seq %d", ev.Seq)
		if i > 0 {
			assert.Equal(t, history[i-1].Digest, ev.PrevDigest, "seq %d links to seq %d", ev.Seq, history[i-1].Seq)
		}
	}
	assert.Equal(t, history[2].PrevDigest, failed.PrevDigest, "the appended event is returned as stored")
	assert.Equal(t, history[2].Digest, failed.Digest, "the appended event is returned as stored")

	// The detail above is stored normalized by JSONB; the chain still verifies.
	require.NoError(t, e.VerifyChain(ctx, pool, c.ID))
}

func TestAppendEvent_IgnoresCallerDigests(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()
	e := tenantTestEngine(t, repo)

	c := openCaseWithEvents(t, pool, "chain-forged")
	forged := make([]byte, 32)
	_, err := pool.Exec(ctx, `
        INSERT INTO workflow_event (id, case_id, seq, kind, actor_id, occurred_at, prev_digest, digest)
        VALUES ($1, $2, 3, 'failed', 'u1', now(), $3, $3)`, uuid.New(), c.ID, forged)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE workflow_case SET last_seq = 3 WHERE id = $1`, c.ID)
	require.NoError(t, err)

	history, err := e.History(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, history, 3)
	assert.Equal(t, history[1].Digest, history[2].PrevDigest, "the trigger sets prev_digest, not the INSERT")
	assert.NotEqual(t, forged, history[2].Digest, "the trigger sets digest, not the INSERT")
	require.NoError(t, e.VerifyChain(ctx, pool, c.ID))
}

// alterInTx runs stmt against case c with the append-only guard disabled,
// verifies the chain inside the same transaction and rolls it back.
func alterInTx(t *testing.T, pool *pgxpool.Pool, e *workflow.Engine, c *workflow.Case, stmt string) error {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `ALTER TABLE workflow_event DISABLE TRIGGER vogel_workflow_event_no_update_delete`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, stmt, c.ID)
	require.NoError(t, err)
	return e.VerifyChain(ctx, tx, c.ID)
}

func TestVerifyChain_DetectsAlteredRow(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()
	e := tenantTestEngine(t, postgres.NewRepository())
	c := openCaseWithEvents(t, pool, "chain-altered")
	require.NoError(t, e.VerifyChain(ctx, pool, c.ID))

	cases := []struct {
		name string
		stmt string
		seq  int64
	}{
		{"actor of the last event", `UPDATE workflow_event SET actor_id = 'mallory' WHERE case_id = $1 AND seq = 2`, 2},
		{"detail of the first event", `UPDATE workflow_event SET detail = '{"x": 1}' WHERE case_id = $1 AND seq = 1`, 1},
		{"schema version", `UPDATE workflow_event SET schema_version = 1 WHERE case_id = $1 AND seq = 2`, 2},
		{"occurred_at", `UPDATE workflow_event SET occurred_at = occurred_at + interval '1 microsecond' WHERE case_id = $1 AND seq = 1`, 1},
		{"removed event", `DELETE FROM workflow_event WHERE case_id = $1 AND seq = 1`, 2},
		{"digest removed", `UPDATE workflow_event SET prev_digest = NULL, digest = NULL WHERE case_id = $1 AND seq = 2`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireChainBrokenAt(t, alterInTx(t, pool, e, c, tc.stmt), c.ID, tc.seq)
		})
	}

	// Every alteration was rolled back, guard included.
	require.NoError(t, e.VerifyChain(ctx, pool, c.ID))
	_, err := pool.Exec(ctx, `UPDATE workflow_event SET actor_id = 'mallory' WHERE case_id = $1`, c.ID)
	requireAppendOnlyRejection(t, err, "UPDATE")
}

func TestVerifyChain_LegacyRowsAreUnchained(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	opts := silentOptions()
	require.NoError(t, migrate.UpTo(ctx, connStr, migrations.FS(), 6, opts))
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// Two events stored before migration 007.
	repo := postgres.NewRepository()
	c := newCase("compras", "chain-legacy")
	c.LastSeq = 2
	require.NoError(t, repo.Create(ctx, pool, c))
	insertLegacyEvent(t, pool, c.ID, 1, workflow.EventOpened)
	insertLegacyEvent(t, pool, c.ID, 2, workflow.EventAssigned)

	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), opts))
	e := tenantTestEngine(t, repo)
	_, err = e.RecordFailure(ctx, pool, workflow.FailureInput{
		CaseID: c.ID, Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}, Action: "submit", Kind: workflow.FailureGuardRejected,
	})
	require.NoError(t, err)

	history, err := e.History(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, history, 3)
	for _, ev := range history[:2] {
		assert.Nil(t, ev.PrevDigest, "legacy seq %d stays unchained", ev.Seq)
		assert.Nil(t, ev.Digest, "legacy seq %d stays unchained", ev.Seq)
	}
	assert.Nil(t, history[2].PrevDigest, "the first event after 007 starts the chain")
	assert.Len(t, history[2].Digest, 32)
	require.NoError(t, e.VerifyChain(ctx, pool, c.ID))
}

func TestVerifyChain_UnknownCaseIsNotFound(t *testing.T) {
	pool := startTestPool(t)
	e := tenantTestEngine(t, postgres.NewRepository())

	require.ErrorIs(t, e.VerifyChain(context.Background(), pool, uuid.New()), workflow.ErrCaseNotFound)
}

func TestAppendEvent_ChainIgnoresShadowingFunctions(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), silentOptions()))
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// Functions named like 007's in a schema that comes first in a session's
	// search_path. The trigger must keep using the ones 007 created.
	_, err = pool.Exec(ctx, `
        CREATE SCHEMA shadow;
        CREATE FUNCTION shadow.vogel_workflow_event_field(b bytea) RETURNS bytea
            LANGUAGE sql IMMUTABLE AS $$ SELECT '\x00'::bytea $$;
        CREATE FUNCTION shadow.vogel_workflow_event_digest(prev bytea, e public.workflow_event) RETURNS bytea
            LANGUAGE sql STABLE AS $$ SELECT '\x00'::bytea $$;`)
	require.NoError(t, err)

	cfg, err := pgxpool.ParseConfig(connStr)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = "shadow, public"
	shadowed, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(shadowed.Close)

	c := openCaseWithEvents(t, shadowed, "chain-shadow")

	e := tenantTestEngine(t, postgres.NewRepository())
	history, err := e.History(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Len(t, history[1].Digest, 32, "the digest is a SHA-256, not the shadow's")
	require.NoError(t, e.VerifyChain(ctx, pool, c.ID), "the stored digests are the ones 007's functions compute")

	// The digest function keeps its own pin when called outside the trigger,
	// as ListEventDigests does from the caller's session.
	var matches bool
	require.NoError(t, shadowed.QueryRow(ctx, `
        SELECT public.vogel_workflow_event_digest(e.prev_digest, e) = e.digest
        FROM public.workflow_event e WHERE e.case_id = $1 AND e.seq = 2`, c.ID).Scan(&matches))
	assert.True(t, matches, "a direct call ignores the shadowing field function")
}

func TestMigration007_UpDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	opts := silentOptions()
	require.NoError(t, migrate.UpTo(ctx, connStr, migrations.FS(), 7, opts))
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	columns := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `
            SELECT count(*) FROM information_schema.columns
            WHERE table_name = 'workflow_event' AND column_name IN ('prev_digest', 'digest')`).Scan(&n))
		return n
	}
	assert.Equal(t, 2, columns())

	require.NoError(t, migrate.Down(ctx, connStr, migrations.FS(), opts))
	assert.Zero(t, columns(), "Down must drop the digest columns")
	var triggers int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname = 'vogel_workflow_event_chain'`).Scan(&triggers))
	assert.Zero(t, triggers, "Down must drop the chain trigger")

	// After Down, a plain insert works again without the chain function.
	c := newCase("compras", "chain-down")
	require.NoError(t, postgres.NewRepository().Create(ctx, pool, c))
	insertLegacyEvent(t, pool, c.ID, 1, workflow.EventOpened)

	require.NoError(t, migrate.UpTo(ctx, connStr, migrations.FS(), 7, opts))
	assert.Equal(t, 2, columns(), "Up must add them again")
}
