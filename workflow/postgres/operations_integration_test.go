//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/workflow"
	"github.com/kafeiih/vogel/workflow/migrations"
)

func TestOverdue_ListsOnlyUnexpiredOverdueTokens(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()
	scheduler := workflow.Actor{Kind: workflow.ActorSystem, ID: "deadline-scheduler"}

	older, _ := tokenTestEngine(t, func() time.Time { return time.Now().Add(-3 * time.Hour) })
	expired, expiredTokens := splitCase(t, pool, older, "tenant-a", "expired")
	old, _ := tokenTestEngine(t, func() time.Time { return time.Now().Add(-2 * time.Hour) })
	_, overdueTokens := splitCase(t, pool, old, "tenant-a", "overdue")
	_, otherTenant := splitCase(t, pool, old, "tenant-b", "other-tenant")
	current, _ := tokenTestEngine(t, nil)
	splitCase(t, pool, current, "tenant-a", "fresh")

	_, err := current.Expire(ctx, pool, workflow.ExpireInput{CaseID: expired.ID, TokenID: expiredTokens["legal"].ID, Actor: scheduler})
	require.NoError(t, err)
	tokens, err := current.Tokens(ctx, pool, expired.ID)
	require.NoError(t, err)
	for _, tok := range tokens {
		if tok.ID == expiredTokens["legal"].ID {
			require.NotNil(t, tok.ExpiredDeadline, "the expired mark round-trips")
			assert.True(t, tok.ExpiredDeadline.Equal(*tok.DeadlineAt))
		}
	}
	_, err = current.Expire(ctx, pool, workflow.ExpireInput{CaseID: expired.ID, TokenID: expiredTokens["legal"].ID, Actor: scheduler})
	require.ErrorIs(t, err, workflow.ErrAlreadyExpired)

	items, err := current.Overdue(ctx, pool, workflow.OverdueFilter{Tenant: "tenant-a"})
	require.NoError(t, err)
	require.Len(t, items, 1, "the expired token, the fresh case and finance (no deadline) are left out")
	assert.Equal(t, overdueTokens["legal"].ID, items[0].Token.ID)
	assert.Equal(t, "tenant-a", items[0].Case.Tenant)

	items, err = current.Overdue(ctx, pool, workflow.OverdueFilter{})
	require.NoError(t, err)
	require.Len(t, items, 2, "an empty tenant matches every tenant")
	assert.Equal(t, overdueTokens["legal"].ID, items[0].Token.ID, "oldest deadline first")
	assert.Equal(t, otherTenant["legal"].ID, items[1].Token.ID)

	// A new deadline, even an already-passed one, can expire again.
	earlier := time.Now().Add(-30 * time.Minute)
	_, err = current.SetDeadline(ctx, pool, workflow.SetDeadlineInput{
		CaseID: expired.ID, TokenID: expiredTokens["legal"].ID, DeadlineAt: &earlier, Actor: workflow.Actor{ID: "op"}, Reason: "corrected",
	})
	require.NoError(t, err)
	items, err = current.Overdue(ctx, pool, workflow.OverdueFilter{Tenant: "tenant-a", Limit: 1})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, overdueTokens["legal"].ID, items[0].Token.ID, "oldest deadline first")
	items, err = current.Overdue(ctx, pool, workflow.OverdueFilter{Tenant: "tenant-a"})
	require.NoError(t, err)
	assert.Len(t, items, 2)

	// Paging with After walks the same order one token at a time, without
	// expiring anything.
	all, err := current.Overdue(ctx, pool, workflow.OverdueFilter{})
	require.NoError(t, err)
	require.Len(t, all, 3)
	var walked []uuid.UUID
	var after *workflow.OverdueCursor
	for range len(all) + 1 {
		page, err := current.Overdue(ctx, pool, workflow.OverdueFilter{Limit: 1, After: after})
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		walked = append(walked, page[0].Token.ID)
		after = page[0].OverdueCursor()
	}
	want := make([]uuid.UUID, 0, len(all))
	for _, it := range all {
		want = append(want, it.Token.ID)
	}
	assert.Equal(t, want, walked)

	// Ties: with every listed deadline equal, the token ID breaks the tie and
	// the cursor still walks each token exactly once.
	tie := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	for _, it := range all {
		_, err := current.SetDeadline(ctx, pool, workflow.SetDeadlineInput{
			CaseID: it.Case.ID, TokenID: it.Token.ID, DeadlineAt: &tie, Actor: workflow.Actor{ID: "op"}, Reason: "tie",
		})
		require.NoError(t, err)
	}
	all, err = current.Overdue(ctx, pool, workflow.OverdueFilter{})
	require.NoError(t, err)
	require.Len(t, all, 3)
	want = want[:0]
	for _, it := range all {
		require.True(t, it.Token.DeadlineAt.Equal(tie))
		want = append(want, it.Token.ID)
	}
	walked, after = nil, nil
	for range len(all) + 1 {
		page, err := current.Overdue(ctx, pool, workflow.OverdueFilter{Limit: 1, After: after})
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		walked = append(walked, page[0].Token.ID)
		after = page[0].OverdueCursor()
	}
	assert.Equal(t, want, walked, "equal deadlines page by token ID")
}

func TestMigration009_UpDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	opts := silentOptions()
	// The engine of this version needs 010 (workflow_event.deadline_at) and 011
	// (workflow_token.outcome), so the data is written at 011 and both are
	// rolled back before 009's Down.
	require.NoError(t, migrate.UpTo(ctx, connStr, migrations.FS(), 11, opts))
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	past, _ := tokenTestEngine(t, func() time.Time { return time.Now().Add(-2 * time.Hour) })
	c, active := splitCase(t, pool, past, "tenant-a", "round-trip")
	current, _ := tokenTestEngine(t, nil)
	_, err = current.Expire(ctx, pool, workflow.ExpireInput{
		CaseID: c.ID, TokenID: active["legal"].ID, Actor: workflow.Actor{Kind: workflow.ActorSystem, ID: "scheduler"},
	})
	require.NoError(t, err)

	column := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `
            SELECT count(*) FROM information_schema.columns
            WHERE table_name = 'workflow_token' AND column_name = 'expired_deadline_at'`).Scan(&n))
		return n
	}
	tokens := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM workflow_token WHERE case_id = $1`, c.ID).Scan(&n))
		return n
	}
	var marked int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM workflow_token WHERE expired_deadline_at IS NOT NULL`).Scan(&marked))
	assert.Equal(t, 1, marked, "Expire wrote the mark")
	before := tokens()
	assert.Equal(t, 1, column())

	require.NoError(t, migrate.Down(ctx, connStr, migrations.FS(), opts)) // 011
	require.NoError(t, migrate.Down(ctx, connStr, migrations.FS(), opts)) // 010
	require.NoError(t, migrate.Down(ctx, connStr, migrations.FS(), opts)) // 009
	assert.Zero(t, column(), "Down drops the column")
	assert.Equal(t, before, tokens(), "Down keeps every token row")

	require.NoError(t, migrate.UpTo(ctx, connStr, migrations.FS(), 9, opts))
	assert.Equal(t, 1, column())
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM workflow_token WHERE expired_deadline_at IS NOT NULL`).Scan(&marked))
	assert.Zero(t, marked, "the mark is lost on Down, as the migration header says")
}
