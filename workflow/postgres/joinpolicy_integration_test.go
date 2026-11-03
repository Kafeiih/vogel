//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"
	"github.com/kafeiih/vogel/workflow/migrations"
	"github.com/kafeiih/vogel/workflow/postgres"
)

// collectTestDefinition sends "draft" to the "juan" and "maria" approvers,
// whose votes meet at a JoinCollect join before the "decide" task.
func collectTestDefinition() workflow.Definition {
	return workflow.Definition{
		Name: "collect", Version: 1,
		Nodes: []workflow.Node{
			{ID: "draft", Start: true},
			{ID: "fork", Kind: workflow.NodeParallel, Join: "vote"},
			{ID: "juan"},
			{ID: "maria"},
			{ID: "vote", Kind: workflow.NodeJoin, JoinPolicy: workflow.JoinCollect},
			{ID: "decide"},
			{ID: "done", Terminal: true},
		},
		Transitions: []workflow.Transition{
			{From: "draft", To: "fork", Action: "send"},
			{From: "fork", To: "juan", Action: "to-juan"},
			{From: "fork", To: "maria", Action: "to-maria"},
			{From: "juan", To: "vote", Action: "approve", Outcome: workflow.OutcomeApproved},
			{From: "juan", To: "vote", Action: "reject", Outcome: workflow.OutcomeRejected},
			{From: "maria", To: "vote", Action: "approve", Outcome: workflow.OutcomeApproved},
			{From: "maria", To: "vote", Action: "reject", Outcome: workflow.OutcomeRejected},
			{From: "vote", To: "decide", Action: "tallied"},
			{From: "decide", To: "done", Action: "finish"},
		},
	}
}

// openToVote opens a case of def and sends it into its branches, returning it
// and the active tokens by node.
func openToVote(t *testing.T, pool *pgxpool.Pool, e *workflow.Engine, def, externalID string) (*workflow.Case, map[string]workflow.Token) {
	t.Helper()
	ctx := context.Background()
	c, err := e.Open(ctx, pool, workflow.OpenInput{Definition: def, Domain: "approvals", ExternalID: externalID, Unit: "hq", Actor: workflow.Actor{ID: "ana"}})
	require.NoError(t, err)
	inTx(t, pool, func(tx pgxtx.DBTX) error {
		_, err := e.Move(ctx, tx, workflow.MoveInput{CaseID: c.ID, Action: "send", Actor: workflow.Actor{ID: "ana"}})
		return err
	})
	tokens, err := e.Tokens(ctx, pool, c.ID)
	require.NoError(t, err)
	active := make(map[string]workflow.Token)
	for _, tok := range tokens {
		if tok.Status == workflow.TokenActive {
			active[tok.Node] = tok
		}
	}
	return c, active
}

func TestMigration011_RoundTrip(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	opts := silentOptions()
	require.NoError(t, migrate.UpTo(ctx, connStr, migrations.FS(), 11, opts))
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	columns := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `
            SELECT count(*) FROM information_schema.columns
            WHERE table_name = 'workflow_token' AND column_name = 'outcome' AND is_nullable = 'YES'`).Scan(&n))
		return n
	}
	assert.Equal(t, 1, columns(), "Up adds the nullable column")

	e := workflow.New(postgres.NewRepository(), workflow.Config{}, nil, workflow.WithDefinitions(collectTestDefinition()))
	c, active := openToVote(t, pool, e, "collect", "migration-011")
	inTx(t, pool, func(tx pgxtx.DBTX) error {
		_, err := e.Move(ctx, tx, workflow.MoveInput{CaseID: c.ID, TokenID: active["juan"].ID, Action: "reject", Actor: workflow.Actor{ID: "juan"}})
		return err
	})
	tokens, err := e.Tokens(ctx, pool, c.ID)
	require.NoError(t, err)
	outcomes := make(map[string]workflow.Outcome)
	for _, tok := range tokens {
		outcomes[tok.Node] = tok.Outcome
	}
	assert.Equal(t, workflow.OutcomeRejected, outcomes["vote"], "the answer round-trips")
	assert.Empty(t, outcomes["fork"], "a NULL outcome reads as empty")
	assert.Empty(t, outcomes["maria"], "a NULL outcome reads as empty")

	require.NoError(t, migrate.Down(ctx, connStr, migrations.FS(), opts))
	assert.Zero(t, columns(), "Down drops the column")
	require.NoError(t, migrate.UpTo(ctx, connStr, migrations.FS(), 11, opts))
	assert.Equal(t, 1, columns())

	// The rollback lost the waiting answer: the round fails closed.
	vote := func(tx pgxtx.DBTX) error {
		_, err := e.Move(ctx, tx, workflow.MoveInput{CaseID: c.ID, TokenID: active["maria"].ID, Action: "approve", Actor: workflow.Actor{ID: "maria"}})
		return err
	}
	require.ErrorIs(t, tryTx(pool, vote), workflow.ErrInvalidOutcome)

	// The statement docs/MIGRATION_GUIDE.md gives restores it from the event.
	_, err = pool.Exec(ctx, `
        UPDATE workflow_token t SET outcome = e.detail->>'outcome'
        FROM workflow_event e
        WHERE e.token_id = t.id AND e.kind = 'waiting' AND t.status = 'waiting'
          AND t.outcome IS NULL AND e.detail ? 'outcome'`)
	require.NoError(t, err)
	inTx(t, pool, vote)
	results, err := e.JoinResults(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, 1, results[0].Count(workflow.OutcomeRejected), "the restored answer counts")
	assert.Equal(t, 1, results[0].Count(workflow.OutcomeApproved))
}

// allApproveTestDefinition sends "draft" to "juan" and "maria" under
// JoinAllApprove; both decisions run the "pause" guard, so a test can hold one
// call between its read and its write.
func allApproveTestDefinition() workflow.Definition {
	return workflow.Definition{
		Name: "all-approve", Version: 1,
		Nodes: []workflow.Node{
			{ID: "draft", Start: true},
			{ID: "fork", Kind: workflow.NodeParallel, Join: "vote"},
			{ID: "juan"},
			{ID: "maria"},
			{ID: "vote", Kind: workflow.NodeJoin, JoinPolicy: workflow.JoinAllApprove},
			{ID: "route", Kind: workflow.NodeDecision},
			{ID: "done", Terminal: true},
		},
		Transitions: []workflow.Transition{
			{From: "draft", To: "fork", Action: "send"},
			{From: "fork", To: "juan", Action: "to-juan"},
			{From: "fork", To: "maria", Action: "to-maria"},
			{From: "juan", To: "vote", Action: "approve", Guard: "pause", Outcome: workflow.OutcomeApproved},
			{From: "juan", To: "vote", Action: "reject", Guard: "pause", Comment: workflow.CommentRequired, Outcome: workflow.OutcomeRejected},
			{From: "maria", To: "vote", Action: "approve", Guard: "pause", Outcome: workflow.OutcomeApproved},
			{From: "maria", To: "vote", Action: "reject", Guard: "pause", Comment: workflow.CommentRequired, Outcome: workflow.OutcomeRejected},
			{From: "vote", To: "route", Action: "tallied"},
			{From: "route", To: "draft", Action: "back", Guard: "rejected"},
			{From: "route", To: "done", Action: "onward"},
		},
	}
}

// TestConcurrentDecisions_FireJoinOnce races a rejection that closes the round
// against the other approver's decision: the case revision lets one win, the
// loser persists nothing, and its retry finds its branch cancelled.
func TestConcurrentDecisions_FireJoinOnce(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()
	p := &pauser{}
	e := workflow.New(postgres.NewRepository(), workflow.Config{}, nil, workflow.WithDefinitions(allApproveTestDefinition()))
	require.NoError(t, e.RegisterGuard("pause", p.guard))
	require.NoError(t, e.RegisterGuard("rejected", workflow.JoinRejected))
	c, active := openToVote(t, pool, e, "all-approve", "race-decisions")

	decide := func(tx pgxtx.DBTX, node, action, comment string) error {
		_, err := e.Move(ctx, tx, workflow.MoveInput{CaseID: c.ID, TokenID: active[node].ID, Action: action, Comment: comment, Actor: workflow.Actor{ID: node}})
		return err
	}

	// Juan's call reads the case, then waits in its guard while María's
	// rejection commits and closes the round.
	p.arm()
	slow := make(chan error, 1)
	go func() { slow <- tryTx(pool, func(tx pgxtx.DBTX) error { return decide(tx, "juan", "approve", "") }) }()
	<-p.entered
	inTx(t, pool, func(tx pgxtx.DBTX) error { return decide(tx, "maria", "reject", "falta el certificado") })
	close(p.release)

	require.ErrorIs(t, <-slow, workflow.ErrConflict, "the slower decision loses the compare-and-swap")
	assert.Equal(t, 1, countKind(t, pool, c.ID, workflow.EventJoined), "the join fired once")
	assert.Equal(t, 1, countKind(t, pool, c.ID, workflow.EventTokenCancelled), "only the winner's cancellation is stored")

	err := tryTx(pool, func(tx pgxtx.DBTX) error { return decide(tx, "juan", "approve", "") })
	require.ErrorIs(t, err, workflow.ErrTokenNotFound, "the retry finds its branch cancelled")

	got, err := postgres.NewRepository().GetByID(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Equal(t, workflow.StatusOpen, got.Status)
	assert.Equal(t, "draft", got.State, "the rejection sent the case back")
	require.NoError(t, e.VerifyChain(ctx, pool, c.ID))
}

// TestVerifyChain_CoversOutcomeDetail proves a branch's stored decision cannot
// change undetected: it lives in the waiting event's detail, which digest v2
// covers, so no digest v3 is needed.
func TestVerifyChain_CoversOutcomeDetail(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()
	e := workflow.New(postgres.NewRepository(), workflow.Config{}, nil, workflow.WithDefinitions(collectTestDefinition()))
	c, active := openToVote(t, pool, e, "collect", "chain-outcome")
	var waitingSeq int64
	inTx(t, pool, func(tx pgxtx.DBTX) error {
		res, err := e.MoveDetailed(ctx, tx, workflow.MoveInput{CaseID: c.ID, TokenID: active["juan"].ID, Action: "reject", Actor: workflow.Actor{ID: "juan"}})
		if err == nil {
			waitingSeq = res.Events[len(res.Events)-1].Seq
		}
		return err
	})
	require.NoError(t, e.VerifyChain(ctx, pool, c.ID))

	err := alterInTx(t, pool, e, c, `UPDATE workflow_event SET detail = '{"outcome":"approved"}' WHERE case_id = $1 AND kind = 'waiting'`)
	requireChainBrokenAt(t, err, c.ID, waitingSeq)
}
