//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"
	"github.com/kafeiih/vogel/workflow/migrations"
	"github.com/kafeiih/vogel/workflow/postgres"
)

// parallelTestDefinition splits "draft" into the "legal" and "finance"
// branches, which meet at "merge" before "done". Both branches' "clear"
// transitions run the "pause" guard, so a test can hold one call between its
// read and its write.
func parallelTestDefinition() workflow.Definition {
	return workflow.Definition{
		Name: "contract", Version: 1,
		Nodes: []workflow.Node{
			{ID: "draft", Start: true, Eligible: workflow.ByPosition("buyer")},
			{ID: "fork", Kind: workflow.NodeParallel, Join: "merge"},
			{ID: "legal", Eligible: workflow.ByPosition("lawyer"), Deadline: time.Hour},
			{ID: "finance", Eligible: workflow.ByPositionInUnit("analyst", "treasury")},
			{ID: "merge", Kind: workflow.NodeJoin},
			{ID: "done", Terminal: true},
		},
		Transitions: []workflow.Transition{
			{From: "draft", To: "fork", Action: "submit"},
			{From: "fork", To: "legal", Action: "to-legal"},
			{From: "fork", To: "finance", Action: "to-finance"},
			{From: "legal", To: "merge", Action: "clear", Guard: "pause"},
			{From: "finance", To: "merge", Action: "clear", Guard: "pause"},
			{From: "merge", To: "done", Action: "merged"},
		},
	}
}

// pauser is the "pause" guard: while armed, the first call it sees signals
// entered and blocks until release is closed.
type pauser struct {
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func (p *pauser) guard(context.Context, pgxtx.DBTX, workflow.Case) (bool, error) {
	if p.armed {
		p.armed = false
		close(p.entered)
		<-p.release
	}
	return true, nil
}

func (p *pauser) arm() {
	p.armed, p.entered, p.release = true, make(chan struct{}), make(chan struct{})
}

func tokenTestEngine(t *testing.T, now func() time.Time) (*workflow.Engine, *pauser) {
	t.Helper()
	p := &pauser{}
	e := workflow.New(postgres.NewRepository(), workflow.Config{Now: now}, nil, workflow.WithDefinitions(parallelTestDefinition()))
	require.NoError(t, e.RegisterGuard("pause", p.guard))
	return e, p
}

// splitCase opens a contract case in tenant and submits it into its two
// branches, returning the case and its active tokens by node.
func splitCase(t *testing.T, pool *pgxpool.Pool, e *workflow.Engine, tenant, externalID string) (*workflow.Case, map[string]workflow.Token) {
	t.Helper()
	ctx := context.Background()
	c, err := e.Open(ctx, pool, workflow.OpenInput{
		Definition: "contract", Tenant: tenant, Domain: "contracts", ExternalID: externalID, Unit: "hq",
		Actor: workflow.Actor{ID: "u1"},
	})
	require.NoError(t, err)
	inTx(t, pool, func(tx pgxtx.DBTX) error {
		_, err := e.Move(ctx, tx, workflow.MoveInput{CaseID: c.ID, Action: "submit", Actor: workflow.Actor{ID: "u1"}})
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
	require.Len(t, active, 2)
	return c, active
}

// inTx runs fn in a transaction it commits, failing the test on any error.
func inTx(t *testing.T, pool *pgxpool.Pool, fn func(tx pgxtx.DBTX) error) {
	t.Helper()
	require.NoError(t, tryTx(pool, fn))
}

// tryTx runs fn in a transaction, committing only when fn succeeds.
func tryTx(pool *pgxpool.Pool, fn func(tx pgxtx.DBTX) error) error {
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func countKind(t *testing.T, pool *pgxpool.Pool, caseID uuid.UUID, kind workflow.EventKind) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM workflow_event WHERE case_id = $1 AND kind = $2`, caseID, string(kind)).Scan(&n))
	return n
}

func TestRepository_TokensAndScopesRoundTrip(t *testing.T) {
	pool := startTestPool(t)
	e, _ := tokenTestEngine(t, nil)
	c, active := splitCase(t, pool, e, "", "round-trip")
	repo := postgres.NewRepository()
	ctx := context.Background()

	scopes, err := repo.ListScopes(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, scopes, 2)
	assert.True(t, scopes[0].IsRoot())
	assert.Equal(t, c.ID, scopes[0].ID)
	assert.Equal(t, workflow.ScopeOpen, scopes[1].Status)
	assert.Equal(t, 2, scopes[1].Expected)
	assert.Equal(t, "merge", scopes[1].JoinNode)
	require.NotNil(t, scopes[1].ParentID)
	assert.Equal(t, c.ID, *scopes[1].ParentID)
	assert.Zero(t, scopes[1].ClosedSeq, "a NULL closed_seq reads as 0")

	tokens, err := repo.ListTokens(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 3)
	assert.Equal(t, c.ID, tokens[0].ID)
	assert.Equal(t, workflow.TokenConsumed, tokens[0].Status)
	assert.NotZero(t, tokens[0].EndedSeq)
	require.NotNil(t, tokens[0].EndedAt)
	assert.Equal(t, workflow.ByPositionInUnit("lawyer", "hq"), active["legal"].Eligible)
	assert.Equal(t, workflow.ByPositionInUnit("analyst", "treasury"), active["finance"].Eligible)
	require.NotNil(t, active["legal"].DeadlineAt)
	assert.Nil(t, active["finance"].DeadlineAt)
	assert.Zero(t, active["legal"].EndedSeq)

	events, err := repo.ListEvents(ctx, pool, c.ID)
	require.NoError(t, err)
	last := events[len(events)-1]
	assert.Equal(t, active["finance"].ID, last.TokenID)
	assert.Equal(t, active["finance"].ScopeID, last.ScopeID)
}

func TestRepository_Integration_ConcurrentLastBranches_JoinExactlyOnce(t *testing.T) {
	pool := startTestPool(t)
	e, p := tokenTestEngine(t, nil)
	c, active := splitCase(t, pool, e, "", "race-join")
	ctx := context.Background()
	moveIn := func(tx pgxtx.DBTX, node string) error {
		_, err := e.Move(ctx, tx, workflow.MoveInput{CaseID: c.ID, TokenID: active[node].ID, Action: "clear", Actor: workflow.Actor{ID: node}})
		return err
	}

	// The finance call reads the case, then waits in its guard while the
	// legal call commits; both are the case's last branches.
	p.arm()
	slow := make(chan error, 1)
	go func() { slow <- tryTx(pool, func(tx pgxtx.DBTX) error { return moveIn(tx, "finance") }) }()
	<-p.entered
	inTx(t, pool, func(tx pgxtx.DBTX) error { return moveIn(tx, "legal") })
	close(p.release)

	err := <-slow
	require.ErrorIs(t, err, workflow.ErrConflict)
	assert.Zero(t, countKind(t, pool, c.ID, workflow.EventJoined), "the conflicting call persisted nothing")

	inTx(t, pool, func(tx pgxtx.DBTX) error { return moveIn(tx, "finance") })
	assert.Equal(t, 1, countKind(t, pool, c.ID, workflow.EventJoined))
	got, err := postgres.NewRepository().GetByID(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Equal(t, workflow.StatusClosed, got.Status)
	assert.Equal(t, "done", got.State)
}

func TestRepository_Integration_DoubleSubmitSameToken(t *testing.T) {
	pool := startTestPool(t)
	e, p := tokenTestEngine(t, nil)
	c, active := splitCase(t, pool, e, "", "double-submit")
	ctx := context.Background()
	legal := active["legal"].ID
	submit := func(tx pgxtx.DBTX) error {
		_, err := e.Move(ctx, tx, workflow.MoveInput{CaseID: c.ID, TokenID: legal, Action: "clear", Actor: workflow.Actor{ID: "u1"}})
		return err
	}

	p.arm()
	slow := make(chan error, 1)
	go func() { slow <- tryTx(pool, submit) }()
	<-p.entered
	inTx(t, pool, submit)
	close(p.release)

	err := <-slow
	require.ErrorIs(t, err, workflow.ErrConflict)
	err = tryTx(pool, submit)
	require.ErrorIs(t, err, workflow.ErrTokenNotFound, "the retry sees the token already waiting")

	var hops int
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT count(*) FROM workflow_event
        WHERE case_id = $1 AND token_id = $2 AND kind = 'moved' AND from_state = 'legal'`, c.ID, legal).Scan(&hops))
	assert.Equal(t, 1, hops, "the token moved on exactly once")
}

func TestRepository_Integration_JoinedOnceIndexRejectsSecondJoin(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()
	c := newCase("compras", "joined-once")
	require.NoError(t, repo.Create(ctx, pool, c))

	scope := uuid.New()
	join := func(seq int64) error {
		return repo.AppendEvent(ctx, pool, &workflow.Event{
			ID: uuid.New(), CaseID: c.ID, Seq: seq, Kind: workflow.EventJoined,
			TokenID: uuid.New(), ScopeID: scope, OccurredAt: time.Now().UTC(),
		})
	}
	require.NoError(t, join(1))
	err := join(2)
	require.ErrorIs(t, err, workflow.ErrConflict, "a second joined event for one scope is refused")
	assert.Equal(t, 1, countKind(t, pool, c.ID, workflow.EventJoined))
}

func TestListEvents_LegacyNullTokenReadsAsCaseID(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()
	c := newCase("compras", "legacy-token")
	require.NoError(t, repo.Create(ctx, pool, c))
	insertLegacyEvent(t, pool, c.ID, 1, workflow.EventOpened)

	events, err := repo.ListEvents(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, c.ID, events[0].TokenID)
	assert.Equal(t, c.ID, events[0].ScopeID)
	var stored *uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT token_id FROM workflow_event WHERE case_id = $1`, c.ID).Scan(&stored))
	assert.Nil(t, stored, "the column stays NULL: nothing is backfilled")
}

func TestInbox_FiltersByEligibilityInSQL(t *testing.T) {
	pool := startTestPool(t)
	e, _ := tokenTestEngine(t, nil)
	c, active := splitCase(t, pool, e, "", "eligibility")
	splitCase(t, pool, e, "", "eligibility-2")
	ctx := context.Background()

	items, err := e.Inbox(ctx, pool, workflow.InboxFilter{Eligibility: []workflow.Eligibility{workflow.ByPositionInUnit("lawyer", "hq")}})
	require.NoError(t, err)
	require.Len(t, items, 2, "one legal token per case")
	for _, it := range items {
		assert.Equal(t, "legal", it.Token.Node)
	}

	items, err = e.Inbox(ctx, pool, workflow.InboxFilter{
		Eligibility: []workflow.Eligibility{workflow.ByPositionInUnit("lawyer", "hq"), workflow.ByPositionInUnit("analyst", "treasury")},
		Limit:       3,
	})
	require.NoError(t, err)
	assert.Len(t, items, 3, "paging counts tokens, not cases")

	items, err = e.Inbox(ctx, pool, workflow.InboxFilter{Eligibility: []workflow.Eligibility{workflow.ByPositionInUnit("analyst", "hq")}})
	require.NoError(t, err)
	assert.Empty(t, items, "the unit must match too")

	_, err = e.ClaimWith(ctx, pool, workflow.ClaimInput{CaseID: c.ID, TokenID: active["finance"].ID, Actor: workflow.Actor{ID: "analyst-1"}})
	require.NoError(t, err)
	items, err = e.Inbox(ctx, pool, workflow.InboxFilter{AssignedTo: "analyst-1"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, active["finance"].ID, items[0].Token.ID)
	assert.Equal(t, c.ID, items[0].Case.ID)
	assert.Equal(t, "", items[0].Case.State, "the case projection is empty while parallel")
}

func TestInbox_UnassignedOverdueAndTenantApplyToTokens(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()
	past, _ := tokenTestEngine(t, func() time.Time { return time.Now().Add(-2 * time.Hour) })
	_, overdue := splitCase(t, pool, past, "tenant-a", "overdue")
	current, _ := tokenTestEngine(t, nil)
	_, fresh := splitCase(t, pool, current, "tenant-b", "fresh")

	items, err := current.Inbox(ctx, pool, workflow.InboxFilter{Overdue: true})
	require.NoError(t, err)
	require.Len(t, items, 1, "only the legal token of the old case is past its deadline")
	assert.Equal(t, overdue["legal"].ID, items[0].Token.ID)

	items, err = current.Inbox(ctx, pool, workflow.InboxFilter{Tenant: "tenant-b"})
	require.NoError(t, err)
	assert.Len(t, items, 2)
	items, err = current.Inbox(ctx, pool, workflow.InboxFilter{Domain: "contracts"})
	require.NoError(t, err)
	assert.Len(t, items, 4, "an empty tenant matches every tenant")

	_, err = current.ClaimWith(ctx, pool, workflow.ClaimInput{CaseID: fresh["legal"].CaseID, TokenID: fresh["legal"].ID, Actor: workflow.Actor{ID: "u9"}})
	require.NoError(t, err)
	items, err = current.Inbox(ctx, pool, workflow.InboxFilter{Tenant: "tenant-b", Unassigned: true})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, fresh["finance"].ID, items[0].Token.ID)
}

// insertEventAt writes an event the way a binary at schema 007 does, entering
// toState.
func insertEventAt(t *testing.T, pool *pgxpool.Pool, caseID uuid.UUID, seq int64, kind workflow.EventKind, toState string, at time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
        INSERT INTO workflow_event (id, case_id, seq, kind, from_state, to_state, action, actor_id, occurred_at, comment, schema_version)
        VALUES ($1, $2, $3, $4, '', $5, '', 'u1', $6, '', 2)`,
		uuid.New(), caseID, seq, string(kind), toState, at)
	require.NoError(t, err)
}

func TestMigration008_BackfillsRootScopeAndToken(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	opts := silentOptions()
	require.NoError(t, migrate.UpTo(ctx, connStr, migrations.FS(), 7, opts))
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	opened := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Microsecond)
	entered := opened.Add(time.Hour)
	deadline := opened.Add(72 * time.Hour)
	openID, closedID := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `
        INSERT INTO workflow_case (id, definition, version, domain, external_id, unit, state, status,
                                   assigned_to, opened_at, closed_at, deadline_at, last_seq)
        VALUES ($1, 'contract', 1, 'contracts', 'open-1', 'hq', 'legal', 'open', 'u7', $3, NULL, $4, 3),
               ($2, 'contract', 1, 'contracts', 'closed-1', 'hq', 'done', 'closed', '', $3, $5, NULL, 4)`,
		openID, closedID, opened, deadline, entered)
	require.NoError(t, err)
	insertEventAt(t, pool, openID, 1, workflow.EventOpened, "draft", opened)
	insertEventAt(t, pool, openID, 2, workflow.EventMoved, "legal", entered)
	insertEventAt(t, pool, openID, 3, workflow.EventAssigned, "legal", entered.Add(time.Minute))

	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), opts))
	repo := postgres.NewRepository()

	tokens, err := repo.ListTokens(ctx, pool, openID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	tok := tokens[0]
	assert.Equal(t, openID, tok.ID)
	assert.Equal(t, openID, tok.ScopeID)
	assert.Equal(t, "legal", tok.Node)
	assert.Equal(t, workflow.TokenActive, tok.Status)
	assert.Equal(t, "u7", tok.AssignedTo)
	require.NotNil(t, tok.DeadlineAt)
	assert.True(t, deadline.Equal(*tok.DeadlineAt))
	assert.Equal(t, int64(2), tok.EnteredSeq, "entered by the moved event that reached its state")
	assert.True(t, entered.Equal(tok.EnteredAt))
	assert.True(t, tok.Eligible.IsZero(), "the migration cannot know a node's eligibility")

	scopes, err := repo.ListScopes(ctx, pool, openID)
	require.NoError(t, err)
	require.Len(t, scopes, 1)
	assert.Equal(t, workflow.ScopeOpen, scopes[0].Status)

	tokens, err = repo.ListTokens(ctx, pool, closedID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.Equal(t, workflow.TokenConsumed, tokens[0].Status)
	assert.Equal(t, int64(4), tokens[0].EndedSeq)
	scopes, err = repo.ListScopes(ctx, pool, closedID)
	require.NoError(t, err)
	assert.Equal(t, workflow.ScopeClosed, scopes[0].Status)

	// The engine carries on from the migrated token, and RefreshEligibility
	// makes it visible to an eligibility filter.
	e, _ := tokenTestEngine(t, nil)
	filter := workflow.InboxFilter{Eligibility: []workflow.Eligibility{workflow.ByPositionInUnit("lawyer", "hq")}}
	items, err := e.Inbox(ctx, pool, filter)
	require.NoError(t, err)
	assert.Empty(t, items)
	wrote, err := e.RefreshEligibility(ctx, pool, openID)
	require.NoError(t, err)
	assert.True(t, wrote)
	items, err = e.Inbox(ctx, pool, filter)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, openID, items[0].Token.ID)
}

func TestEngine_SynthesizesRootTokenForCaseWrittenWithoutTokens(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()
	// An older binary created the case after migration 008: no token rows.
	c := newCase("contracts", "old-binary")
	c.Definition, c.State, c.LastSeq = "contract", "draft", 1
	require.NoError(t, postgres.NewRepository().Create(ctx, pool, c))
	insertLegacyEvent(t, pool, c.ID, 1, workflow.EventOpened)

	e, _ := tokenTestEngine(t, nil)
	inTx(t, pool, func(tx pgxtx.DBTX) error {
		_, err := e.Move(ctx, tx, workflow.MoveInput{CaseID: c.ID, Action: "submit", Actor: workflow.Actor{ID: "u1"}})
		return err
	})
	tokens, err := postgres.NewRepository().ListTokens(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 3, "the synthesized root token was persisted, then split")
	assert.Equal(t, c.ID, tokens[0].ID)
	assert.Equal(t, workflow.TokenConsumed, tokens[0].Status)
}

func TestMigration008_UpDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	opts := silentOptions()
	require.NoError(t, migrate.UpTo(ctx, connStr, migrations.FS(), 8, opts))
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tables := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `
            SELECT count(*) FROM information_schema.tables
            WHERE table_name IN ('workflow_token', 'workflow_scope')`).Scan(&n))
		return n
	}
	assert.Equal(t, 2, tables())
	require.NoError(t, migrate.Down(ctx, connStr, migrations.FS(), opts))
	assert.Zero(t, tables(), "Down drops both tables")
	var columns int
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT count(*) FROM information_schema.columns
        WHERE table_name = 'workflow_event' AND column_name IN ('token_id', 'scope_id')`).Scan(&columns))
	assert.Zero(t, columns, "Down drops the event columns")
	require.NoError(t, migrate.UpTo(ctx, connStr, migrations.FS(), 8, opts))
	assert.Equal(t, 2, tables())
}

// TestCancel_PersistsCancelledStatus checks the new status round-trips.
func TestCancel_PersistsCancelledStatus(t *testing.T) {
	pool := startTestPool(t)
	e, _ := tokenTestEngine(t, nil)
	c, _ := splitCase(t, pool, e, "", "cancel")
	ctx := context.Background()
	inTx(t, pool, func(tx pgxtx.DBTX) error {
		_, err := e.Cancel(ctx, tx, workflow.CancelInput{CaseID: c.ID, Actor: workflow.Actor{ID: "u1"}, Reason: "withdrawn"})
		return err
	})
	got, err := postgres.NewRepository().GetByID(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Equal(t, workflow.StatusCancelled, got.Status)
	assert.Equal(t, 2, countKind(t, pool, c.ID, workflow.EventTokenCancelled))
	items, err := e.Inbox(ctx, pool, workflow.InboxFilter{})
	require.NoError(t, err)
	assert.Empty(t, items)
	_, err = e.Move(ctx, pool, workflow.MoveInput{CaseID: c.ID, Action: "clear", Actor: workflow.Actor{ID: "u1"}})
	assert.True(t, errors.Is(err, workflow.ErrCaseClosed))
}
