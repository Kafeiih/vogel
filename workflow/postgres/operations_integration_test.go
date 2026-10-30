//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/workflow"
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
}
