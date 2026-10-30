package workflow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReassign_RecordsFromToReasonAndDispatchesNoEffects(t *testing.T) {
	d := testDefinition()
	d.Nodes[0].Effects = []string{"notify-owner"} // draft
	sink := newRecordingSink("notify-owner")
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithEffectSink(sink), WithDefinitions(d))
	c := openTestCase(t, e)
	_, err := e.Claim(context.Background(), nil, c.ID, "buyer-1")
	require.NoError(t, err)
	require.Equal(t, 1, sink.callCount(), "Open dispatched the draft entry effect")

	got, err := e.Reassign(context.Background(), nil, ReassignInput{
		CaseID: c.ID, To: "buyer-2", Actor: Actor{Kind: ActorOperator, ID: "supervisor-1"}, Reason: "  on leave  ",
	})
	require.NoError(t, err)
	assert.Equal(t, "buyer-2", got.AssignedTo)
	assert.Equal(t, "draft", got.State)

	events := storedEvents(t, repo, c.ID)
	last := events[len(events)-1]
	require.Equal(t, EventAssigned, last.Kind)
	assert.Equal(t, Actor{Kind: ActorOperator, ID: "supervisor-1"}, last.Actor)
	assert.Equal(t, "draft", last.FromState)
	assert.Equal(t, "draft", last.ToState)
	assert.Equal(t, AssignmentDetail{From: "buyer-1", To: "buyer-2", Reason: "on leave"}, decodeAssignment(t, last))
	assert.Equal(t, 1, sink.callCount(), "Reassign never dispatches effects, so entry actions don't re-run")

	stored, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "buyer-2", stored.AssignedTo)
}

func TestReassign_RequiresReason(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	c := openTestCase(t, e)
	before := storedEvents(t, repo, c.ID)

	for _, reason := range []string{"", "   "} {
		_, err := e.Reassign(context.Background(), nil, ReassignInput{CaseID: c.ID, To: "buyer-2", Actor: Actor{ID: "supervisor-1"}, Reason: reason})
		require.ErrorIs(t, err, ErrReasonRequired)
	}
	_, err := e.Reassign(context.Background(), nil, ReassignInput{CaseID: c.ID, To: "", Actor: Actor{ID: "supervisor-1"}, Reason: "x"})
	require.ErrorIs(t, err, ErrInvalidActor, "the new assignee needs an ID")
	assert.Equal(t, before, storedEvents(t, repo, c.ID), "nothing is persisted")
}

func TestReassign_PerToken(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	_, err := e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, TokenID: active["finance"].ID, Actor: Actor{ID: "analyst-1"}})
	require.NoError(t, err)

	_, err = e.Reassign(context.Background(), nil, ReassignInput{CaseID: c.ID, To: "lawyer-2", Actor: Actor{ID: "supervisor-1"}, Reason: "x"})
	require.ErrorIs(t, err, ErrTokenRequired, "a case with several active tokens needs TokenID")

	_, err = e.Reassign(context.Background(), nil, ReassignInput{
		CaseID: c.ID, TokenID: active["legal"].ID, To: "lawyer-2", Actor: Actor{ID: "supervisor-1"}, Reason: "workload",
	})
	require.NoError(t, err)
	assertInvariants(t, e, repo, c.ID)

	now := activeTokens(t, e, c.ID)
	assert.Equal(t, "lawyer-2", now["legal"].AssignedTo)
	assert.Equal(t, "analyst-1", now["finance"].AssignedTo, "the other branch keeps its assignee")

	events := storedEvents(t, repo, c.ID)
	last := events[len(events)-1]
	assert.Equal(t, active["legal"].ID, last.TokenID)
	assert.Equal(t, AssignmentDetail{From: "", To: "lawyer-2", Reason: "workload"}, decodeAssignment(t, last))
}

func TestRelease_ByOtherActorRequiresReason(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	c := openTestCase(t, e)
	_, err := e.Claim(context.Background(), nil, c.ID, "buyer-1")
	require.NoError(t, err)
	before := storedEvents(t, repo, c.ID)

	for _, reason := range []string{"", "  "} {
		_, err = e.ReleaseWith(context.Background(), nil, ReleaseInput{CaseID: c.ID, Actor: Actor{Kind: ActorOperator, ID: "supervisor-1"}, Reason: reason})
		require.ErrorIs(t, err, ErrReasonRequired)
	}
	_, err = e.Release(context.Background(), nil, c.ID, "buyer-2")
	require.ErrorIs(t, err, ErrReasonRequired, "the deprecated wrapper carries no reason")
	assert.Equal(t, before, storedEvents(t, repo, c.ID), "nothing is persisted")

	_, err = e.ReleaseWith(context.Background(), nil, ReleaseInput{CaseID: c.ID, Actor: Actor{Kind: ActorOperator, ID: "supervisor-1"}, Reason: "on leave"})
	require.NoError(t, err)
}

func TestRelease_SelfReleaseNeedsNoReason(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	c := openTestCase(t, e)
	_, err := e.Claim(context.Background(), nil, c.ID, "buyer-1")
	require.NoError(t, err)

	released, err := e.Release(context.Background(), nil, c.ID, "buyer-1")
	require.NoError(t, err)
	assert.Empty(t, released.AssignedTo)
	events := storedEvents(t, repo, c.ID)
	assert.Equal(t, AssignmentDetail{From: "buyer-1"}, decodeAssignment(t, events[len(events)-1]))
}

func revision(v int64) *int64 { return &v }

func TestMove_ExpectedRevisionMismatchIsConflictAndPersistsNothing(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	c := openTestCase(t, e)
	_, err := e.Claim(context.Background(), nil, c.ID, "buyer-1") // revision 0 -> 1
	require.NoError(t, err)
	before := storedEvents(t, repo, c.ID)
	stored, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), stored.Revision)

	// The stale view would also fail later checks (unknown action): the
	// revision is checked first.
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "nope", ActorID: "buyer-1", ExpectedRevision: revision(0)})
	require.ErrorIs(t, err, ErrConflict)
	require.NotErrorIs(t, err, ErrInvalidTransition)

	after, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, *stored, *after, "the case is untouched")
	assert.Equal(t, before, storedEvents(t, repo, c.ID), "no event is appended")

	got, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "buyer-1", ExpectedRevision: revision(1)})
	require.NoError(t, err)
	assert.Equal(t, "review", got.State)
}

func TestClaim_ExpectedRevisionMatchProceeds(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	c := openTestCase(t, e)

	got, err := e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, Actor: Actor{ID: "buyer-1"}, ExpectedRevision: revision(0)})
	require.NoError(t, err)
	assert.Equal(t, "buyer-1", got.AssignedTo)
	assert.Equal(t, int64(1), got.Revision)

	_, err = e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, Actor: Actor{ID: "buyer-1"}, ExpectedRevision: revision(0)})
	require.ErrorIs(t, err, ErrConflict, "a stale view fails even where the call would be a no-op")
}

// TestExpectedRevision_MismatchIsConflictOnEveryInput covers the other
// inputs that carry ExpectedRevision.
func TestExpectedRevision_MismatchIsConflictOnEveryInput(t *testing.T) {
	stale := revision(7)
	for name, call := range map[string]func(e *Engine, c *Case) error{
		"release": func(e *Engine, c *Case) error {
			_, err := e.ReleaseWith(context.Background(), nil, ReleaseInput{CaseID: c.ID, Actor: Actor{ID: "buyer-1"}, ExpectedRevision: stale})
			return err
		},
		"reassign": func(e *Engine, c *Case) error {
			_, err := e.Reassign(context.Background(), nil, ReassignInput{CaseID: c.ID, To: "buyer-2", Actor: Actor{ID: "s"}, Reason: "x", ExpectedRevision: stale})
			return err
		},
		"cancel": func(e *Engine, c *Case) error {
			_, err := e.Cancel(context.Background(), nil, CancelInput{CaseID: c.ID, Actor: Actor{ID: "s"}, Reason: "x", ExpectedRevision: stale})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newFakeRepository()
			e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
			c := openTestCase(t, e)
			_, err := e.Claim(context.Background(), nil, c.ID, "buyer-1")
			require.NoError(t, err)
			before := storedEvents(t, repo, c.ID)

			require.ErrorIs(t, call(e, c), ErrConflict)
			assert.Equal(t, before, storedEvents(t, repo, c.ID))
		})
	}
}
