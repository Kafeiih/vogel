package action_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/worker"
	"github.com/kafeiih/vogel/workflow"
)

// fakeTx is a pgx.Tx whose methods are never called: the Registry only
// passes it through to the Queue.
type fakeTx struct{ pgx.Tx }

// enqueued records one EnqueueTx call.
type enqueued struct {
	tx   pgx.Tx
	args action.Args
	opts *river.InsertOpts
}

// recordingQueue is a worker.Queue that records EnqueueTx calls and fails
// the test on any non-transactional Enqueue.
type recordingQueue struct {
	t     *testing.T
	calls []enqueued
}

var _ worker.Queue = (*recordingQueue)(nil)

func (q *recordingQueue) Enqueue(context.Context, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	q.t.Fatal("Enqueue outside a transaction must never be called")
	return nil, nil
}

func (q *recordingQueue) EnqueueTx(_ context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	a, ok := args.(action.Args)
	if !ok {
		q.t.Fatalf("unexpected job args type %T", args)
	}
	q.calls = append(q.calls, enqueued{tx: tx, args: a, opts: opts})
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

func (q *recordingQueue) Start(context.Context) error { return nil }
func (q *recordingQueue) Stop(context.Context) error  { return nil }

func nopHandler() action.Handler {
	return action.HandlerFunc(func(context.Context, action.Run) error { return nil })
}

func newRegistry(t *testing.T, names ...string) (*action.Registry, *recordingQueue) {
	t.Helper()
	q := &recordingQueue{t: t}
	r := action.NewRegistry(q)
	for _, n := range names {
		require.NoError(t, r.Register(n, nopHandler()))
	}
	return r, q
}

// effectsDefinition: start node "draft" (effect "on_draft") → submit (effect
// "on_submit") → decision "route" → route "big" (effect "on_big") → terminal
// "approved" (effect "on_approved"); route "small" (default) → "review".
func effectsDefinition() workflow.Definition {
	return workflow.Definition{
		Name:    "purchase",
		Version: 3,
		Nodes: []workflow.Node{
			{ID: "draft", Start: true, Effects: []string{"on_draft"}},
			{ID: "route", Kind: workflow.NodeDecision},
			{ID: "review", Effects: []string{"on_review"}},
			{ID: "approved", Terminal: true, Effects: []string{"on_approved"}},
		},
		Transitions: []workflow.Transition{
			{From: "draft", To: "route", Action: "submit", Effects: []string{"on_submit", "audit"}},
			{From: "route", To: "approved", Action: "big", Guard: "is_big", Effects: []string{"on_big"}},
			{From: "route", To: "review", Action: "small"},
			{From: "review", To: "approved", Action: "approve"},
		},
	}
}

func testCase() workflow.Case {
	return workflow.Case{
		ID:         uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Definition: "purchase",
		Version:    3,
		Domain:     "compras",
		ExternalID: "solicitud-1",
		Unit:       "finanzas",
	}
}

func effectsOf(calls []enqueued) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.args.Effect)
	}
	return out
}

func TestDispatch_Opened_EnqueuesStartNodeEffects(t *testing.T) {
	r, q := newRegistry(t, "on_draft")
	tx := fakeTx{}
	c := testCase()
	events := []workflow.Event{{CaseID: c.ID, Seq: 1, Kind: workflow.EventOpened, ToState: "draft", ActorID: "u1"}}

	require.NoError(t, r.Dispatch(context.Background(), tx, effectsDefinition(), c, events))

	require.Len(t, q.calls, 1)
	got := q.calls[0]
	assert.Equal(t, pgx.Tx(tx), got.tx, "must enqueue on the caller's transaction")
	assert.Equal(t, action.Args{
		CaseID:     c.ID,
		Seq:        1,
		Effect:     "on_draft",
		Definition: "purchase",
		Version:    3,
		Domain:     "compras",
		ExternalID: "solicitud-1",
		Unit:       "finanzas",
		ActorID:    "u1",
	}, got.args)
	assert.Equal(t, "vogel_action", got.args.Kind())
	require.NotNil(t, got.opts)
	require.NotNil(t, got.opts.UniqueOpts.ByArgs)
	assert.True(t, got.opts.UniqueOpts.ByArgs, "jobs must be unique by (case id, seq, effect)")
}

func TestDispatch_CopiesStructuredActor(t *testing.T) {
	r, q := newRegistry(t, "on_draft")
	c := testCase()
	actor := workflow.Actor{Kind: workflow.ActorOperator, ID: "u1", ActingAs: "jefe@finanzas", OnBehalfOf: "u9", Via: "backoffice"}
	events := []workflow.Event{{CaseID: c.ID, Seq: 1, Kind: workflow.EventOpened, ToState: "draft", Actor: actor, ActorID: "u1"}}

	require.NoError(t, r.Dispatch(context.Background(), fakeTx{}, effectsDefinition(), c, events))

	require.Len(t, q.calls, 1)
	args := q.calls[0].args
	assert.Equal(t, actor, args.Actor)
	assert.Equal(t, "u1", args.ActorID, "ActorID stays, mirroring Actor.ID")
}

func TestDispatch_Moved_EnqueuesTransitionThenRouteEffectsInSeqOrder(t *testing.T) {
	r, q := newRegistry(t, "on_submit", "audit", "on_big", "on_approved")
	c := testCase()
	events := []workflow.Event{
		{Seq: 4, Kind: workflow.EventMoved, FromState: "draft", ToState: "route", Action: "submit", ActorID: "u2"},
		{Seq: 5, Kind: workflow.EventMoved, FromState: "route", ToState: "approved", Action: "big", ActorID: "u2"},
		{Seq: 6, Kind: workflow.EventClosed, FromState: "approved", ToState: "approved", Action: "big", ActorID: "u2"},
	}

	require.NoError(t, r.Dispatch(context.Background(), fakeTx{}, effectsDefinition(), c, events))

	// Per event: destination node effects, then the taken transition's.
	// "route" is a decision node (no effects); EventClosed adds nothing.
	assert.Equal(t, []string{"on_submit", "audit", "on_approved", "on_big"}, effectsOf(q.calls))
	seqs := make([]int64, 0, len(q.calls))
	for _, call := range q.calls {
		seqs = append(seqs, call.args.Seq)
		assert.Equal(t, "u2", call.args.ActorID)
	}
	assert.Equal(t, []int64{4, 4, 5, 5}, seqs)
}

func TestDispatch_Moved_EnqueuesDestinationNodeEffects(t *testing.T) {
	r, q := newRegistry(t, "on_review")
	events := []workflow.Event{
		{Seq: 2, Kind: workflow.EventMoved, FromState: "route", ToState: "review", Action: "small"},
	}

	require.NoError(t, r.Dispatch(context.Background(), fakeTx{}, effectsDefinition(), testCase(), events))

	assert.Equal(t, []string{"on_review"}, effectsOf(q.calls))
}

func TestDispatch_ClosedAndAssignmentEventsAddNothing(t *testing.T) {
	r, q := newRegistry(t, "on_approved")
	events := []workflow.Event{
		{Seq: 7, Kind: workflow.EventClosed, FromState: "approved", ToState: "approved", Action: "approve"},
		{Seq: 8, Kind: workflow.EventAssigned, FromState: "approved", ToState: "approved"},
	}

	require.NoError(t, r.Dispatch(context.Background(), fakeTx{}, effectsDefinition(), testCase(), events))

	assert.Empty(t, q.calls)
}

func TestDispatch_NoTransaction_ReturnsErrNoTxAndEnqueuesNothing(t *testing.T) {
	r, q := newRegistry(t, "on_draft")
	events := []workflow.Event{{Seq: 1, Kind: workflow.EventOpened, ToState: "draft"}}

	// A pool is a DBTX but not a transaction, and ctx carries none.
	var pool *pgxpool.Pool
	err := r.Dispatch(context.Background(), pool, effectsDefinition(), testCase(), events)

	require.ErrorIs(t, err, pgxtx.ErrNoTx)
	assert.Empty(t, q.calls)
}

func TestDispatch_NoEffects_NeedsNoTransaction(t *testing.T) {
	r, q := newRegistry(t)
	d := workflow.Definition{Name: "plain", Version: 1, Nodes: []workflow.Node{{ID: "a", Start: true}}}
	events := []workflow.Event{{Seq: 1, Kind: workflow.EventOpened, ToState: "a"}}

	var pool *pgxpool.Pool
	require.NoError(t, r.Dispatch(context.Background(), pool, d, testCase(), events))
	assert.Empty(t, q.calls)
}

func TestDispatch_UnregisteredEffect_ReturnsErrUnknownEffect(t *testing.T) {
	r, q := newRegistry(t)
	events := []workflow.Event{{Seq: 1, Kind: workflow.EventOpened, ToState: "draft"}}

	err := r.Dispatch(context.Background(), fakeTx{}, effectsDefinition(), testCase(), events)

	require.ErrorIs(t, err, workflow.ErrUnknownEffect)
	assert.Empty(t, q.calls)
}

func TestRegister_RejectsEmptyNameNilHandlerAndDuplicate(t *testing.T) {
	r, _ := newRegistry(t)

	require.ErrorIs(t, r.Register("", nopHandler()), action.ErrInvalidRegistration)
	require.ErrorIs(t, r.Register("notify", nil), action.ErrInvalidRegistration)
	require.NoError(t, r.Register("notify", nopHandler()))
	require.ErrorIs(t, r.Register("notify", nopHandler()), action.ErrDuplicateEffect)

	assert.True(t, r.HasEffect("notify"))
	assert.False(t, r.HasEffect(""))
}

func TestHasEffect_DrivesEngineRegister(t *testing.T) {
	r, _ := newRegistry(t, "on_draft", "on_submit", "audit", "on_big", "on_approved")
	e := workflow.New(nil, workflow.Config{}, nil, workflow.WithEffectSink(r))

	err := e.Register(effectsDefinition())

	require.ErrorIs(t, err, workflow.ErrUnknownEffect, "on_review is not registered")
	assert.Contains(t, err.Error(), "on_review")

	require.NoError(t, r.Register("on_review", nopHandler()))
	require.NoError(t, e.Register(effectsDefinition()))
}

func TestDispatch_PropagatesEnqueueError(t *testing.T) {
	boom := errors.New("boom")
	q := &failingQueue{err: boom}
	r := action.NewRegistry(q)
	require.NoError(t, r.Register("on_draft", nopHandler()))
	events := []workflow.Event{{Seq: 1, Kind: workflow.EventOpened, ToState: "draft"}}

	err := r.Dispatch(context.Background(), fakeTx{}, effectsDefinition(), testCase(), events)

	require.ErrorIs(t, err, boom)
}

type failingQueue struct {
	recordingQueue
	err error
}

func (q *failingQueue) EnqueueTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, q.err
}
