package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

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

// clockEngine returns an engine over testDefinition (with mutate applied)
// whose clock reads *now, and a case moved to "review", whose deadline is
// *now + 48h at that point.
func clockEngine(t *testing.T, now *time.Time, mutate func(*Definition), opts ...Option) (*Engine, *fakeRepository, *Case) {
	t.Helper()
	d := testDefinition()
	if mutate != nil {
		mutate(&d)
	}
	repo := newFakeRepository()
	e := New(repo, Config{Now: func() time.Time { return *now }}, testLogger(), append(opts, WithDefinitions(d))...)
	c := openTestCase(t, e)
	c, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "buyer-1"})
	require.NoError(t, err)
	require.Equal(t, "review", c.State)
	return e, repo, c
}

var scheduler = Actor{Kind: ActorSystem, ID: "deadline-scheduler"}

func TestSetDeadline_RecordsOldAndNew(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	e, repo, c := clockEngine(t, &now, nil)
	old := now.Add(48 * time.Hour)
	extended := now.Add(96 * time.Hour)

	_, err := e.SetDeadline(context.Background(), nil, SetDeadlineInput{CaseID: c.ID, DeadlineAt: &extended, Actor: Actor{ID: "s"}, Reason: " "})
	require.ErrorIs(t, err, ErrReasonRequired)

	got, err := e.SetDeadline(context.Background(), nil, SetDeadlineInput{
		CaseID: c.ID, DeadlineAt: &extended, Actor: Actor{Kind: ActorOperator, ID: "supervisor-1"}, Reason: "missing documents",
	})
	require.NoError(t, err)
	require.NotNil(t, got.DeadlineAt)
	assert.Equal(t, extended, *got.DeadlineAt, "the case shows the token's new deadline")

	events := storedEvents(t, repo, c.ID)
	last := events[len(events)-1]
	require.Equal(t, EventDeadlineChanged, last.Kind)
	assert.Equal(t, Actor{Kind: ActorOperator, ID: "supervisor-1"}, last.Actor)
	var detail DeadlineDetail
	require.NoError(t, json.Unmarshal(last.Detail, &detail))
	require.NotNil(t, detail.From)
	require.NotNil(t, detail.To)
	assert.True(t, old.Equal(*detail.From))
	assert.True(t, extended.Equal(*detail.To))
	assert.Equal(t, "missing documents", detail.Reason)

	got, err = e.SetDeadline(context.Background(), nil, SetDeadlineInput{CaseID: c.ID, Actor: Actor{ID: "s"}, Reason: "suspended", ExpectedRevision: revision(got.Revision)})
	require.NoError(t, err)
	assert.Nil(t, got.DeadlineAt, "a nil deadline removes it")
	tokens, err := e.Tokens(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Nil(t, tokens[0].DeadlineAt)

	_, err = e.SetDeadline(context.Background(), nil, SetDeadlineInput{CaseID: c.ID, Actor: Actor{ID: "s"}, Reason: "x", ExpectedRevision: revision(0)})
	require.ErrorIs(t, err, ErrConflict)
	assertInvariants(t, e, repo, c.ID)
}

func TestSetDeadline_ExtensionAllowsExpiringAgain(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	e, repo, c := clockEngine(t, &now, nil)
	now = now.Add(49 * time.Hour)
	_, err := e.Expire(context.Background(), nil, ExpireInput{CaseID: c.ID, Actor: scheduler})
	require.NoError(t, err)
	assertInvariants(t, e, repo, c.ID)

	extended := now.Add(time.Hour)
	_, err = e.SetDeadline(context.Background(), nil, SetDeadlineInput{CaseID: c.ID, DeadlineAt: &extended, Actor: Actor{ID: "s"}, Reason: "extension"})
	require.NoError(t, err)
	_, err = e.Expire(context.Background(), nil, ExpireInput{CaseID: c.ID, Actor: scheduler})
	require.ErrorIs(t, err, ErrNotOverdue)

	now = now.Add(2 * time.Hour)
	_, err = e.Expire(context.Background(), nil, ExpireInput{CaseID: c.ID, Actor: scheduler})
	require.NoError(t, err, "the extended deadline expires again")
	assertInvariants(t, e, repo, c.ID)
}

func TestExpire_BeforeDeadlineIsNotOverdue(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	e, repo, c := clockEngine(t, &now, nil)
	before := storedEvents(t, repo, c.ID)

	now = now.Add(47 * time.Hour)
	_, err := e.Expire(context.Background(), nil, ExpireInput{CaseID: c.ID, Actor: scheduler})
	require.ErrorIs(t, err, ErrNotOverdue)
	assert.Equal(t, before, storedEvents(t, repo, c.ID))

	draft := openInTenant(t, e, "", "no-deadline")
	_, err = e.Expire(context.Background(), nil, ExpireInput{CaseID: draft.ID, Actor: scheduler})
	require.ErrorIs(t, err, ErrNotOverdue, "a token without a deadline is never overdue")

	now = now.Add(time.Hour) // exactly the deadline
	_, err = e.Expire(context.Background(), nil, ExpireInput{CaseID: c.ID, Actor: scheduler})
	require.NoError(t, err)
}

func TestExpire_RecordsExpiredOnce(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	e, repo, c := clockEngine(t, &now, nil)
	deadline := now.Add(48 * time.Hour)
	now = now.Add(50 * time.Hour)

	res, err := e.Expire(context.Background(), nil, ExpireInput{CaseID: c.ID, Actor: scheduler})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	evt := res.Events[0]
	assert.Equal(t, EventExpired, evt.Kind)
	assert.Equal(t, scheduler, evt.Actor)
	assert.Equal(t, c.ID, evt.TokenID)
	assert.Equal(t, "review", evt.FromState)
	var detail ExpiryDetail
	require.NoError(t, json.Unmarshal(evt.Detail, &detail))
	assert.True(t, deadline.Equal(detail.DeadlineAt))
	assert.Equal(t, "review", res.Case.State, "without OnExpire the token stays, visibly overdue")
	require.NotNil(t, res.Case.DeadlineAt)

	before := storedEvents(t, repo, c.ID)
	_, err = e.Expire(context.Background(), nil, ExpireInput{CaseID: c.ID, Actor: scheduler})
	require.ErrorIs(t, err, ErrAlreadyExpired, "a scheduler retry is idempotent")
	assert.Equal(t, before, storedEvents(t, repo, c.ID))
	assertInvariants(t, e, repo, c.ID)
}

func TestExpire_TakesOnExpireTransitionAsSystem(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	sink := newRecordingSink("archive")
	e, repo, c := clockEngine(t, &now, func(d *Definition) {
		d.Nodes[1].OnExpire = "reject"                 // review
		d.Transitions[2].Comment = CommentRequired     // review -> rejected
		d.Transitions[2].Effects = []string{"archive"} // review -> rejected
	}, WithEffectSink(sink))
	now = now.Add(49 * time.Hour)

	res, err := e.Expire(context.Background(), nil, ExpireInput{CaseID: c.ID, Actor: scheduler})
	require.NoError(t, err)
	assert.Equal(t, []EventKind{EventExpired, EventMoved, EventClosed}, eventKinds(res.Events))
	for _, evt := range res.Events {
		assert.Equal(t, scheduler, evt.Actor)
	}
	moved := res.Events[1]
	assert.Equal(t, "reject", moved.Action)
	assert.Equal(t, "review", moved.FromState)
	assert.Equal(t, "rejected", moved.ToState)
	assert.JSONEq(t, `{"cause":"expired"}`, string(moved.Detail))
	assert.Equal(t, StatusClosed, res.Case.Status)
	assert.Equal(t, res.Events, storedEvents(t, repo, c.ID)[len(storedEvents(t, repo, c.ID))-3:])
	assert.Equal(t, eventKinds(res.Events), eventKinds(sink.lastCall(t).events), "the transition's effects are dispatched")
	assertInvariants(t, e, repo, c.ID)
}

func TestExpire_RequiresSystemActor(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	e, _, c := clockEngine(t, &now, nil)
	now = now.Add(49 * time.Hour)
	for _, actor := range []Actor{{ID: "u1"}, {Kind: ActorOperator, ID: "op"}, {Kind: ActorSystem}} {
		_, err := e.Expire(context.Background(), nil, ExpireInput{CaseID: c.ID, Actor: actor})
		require.ErrorIs(t, err, ErrInvalidActor, "%+v", actor)
	}
}

func TestValidate_OnExpireMustNameOutgoingNonReturnTransition(t *testing.T) {
	d := testDefinition()
	d.Nodes[1].OnExpire = "submit" // a transition, but not one leaving review
	require.ErrorIs(t, d.Validate(), ErrInvalidDefinition)
	assert.Contains(t, d.Validate().Error(), `node "review": on_expire "submit" is not an outgoing transition`)

	d = testDefinition()
	d.Transitions = append(d.Transitions, Transition{From: "review", To: "draft", Action: "send-back", Return: true})
	d.Nodes[1].OnExpire = "send-back"
	require.ErrorIs(t, d.Validate(), ErrInvalidDefinition)
	assert.Contains(t, d.Validate().Error(), `node "review": on_expire "send-back" must not be a return transition`)

	d = testDefinition()
	d.Nodes[1].OnExpire = "reject"
	require.NoError(t, d.Validate())
}

func TestOverdue_PagesOldestFirst(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	repo := newFakeRepository()
	e := New(repo, Config{Now: func() time.Time { return now }}, testLogger(), WithDefinitions(testDefinition()))
	ids := make([]uuid.UUID, 0, 3)
	for i := range 3 {
		c := openInTenant(t, e, "t1", fmt.Sprintf("case-%d", i))
		_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
		require.NoError(t, err)
		ids = append(ids, c.ID)
		now = now.Add(-time.Hour) // each later case has an earlier deadline
	}
	other := openInTenant(t, e, "t2", "other")
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: other.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	now = now.Add(100 * time.Hour)
	page, err := e.Overdue(context.Background(), nil, OverdueFilter{Tenant: "t1", Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{ids[2], ids[1]}, workItemCaseIDs(page), "oldest deadline first")

	// The scheduler expires what it listed; the next call returns the rest.
	for _, it := range page {
		_, err := e.Expire(context.Background(), nil, ExpireInput{CaseID: it.Case.ID, TokenID: it.Token.ID, Actor: scheduler})
		require.NoError(t, err)
	}
	page, err = e.Overdue(context.Background(), nil, OverdueFilter{Tenant: "t1", Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{ids[0]}, workItemCaseIDs(page))

	page, err = e.Overdue(context.Background(), nil, OverdueFilter{Tenant: "t1", Before: now.Add(-1000 * time.Hour)})
	require.NoError(t, err)
	assert.Empty(t, page, "Before bounds the deadlines listed")
}

// resolverFunc adapts a function to DeadlineResolver.
type resolverFunc func(ctx context.Context, in DueInput) (time.Time, error)

func (f resolverFunc) Due(ctx context.Context, in DueInput) (time.Time, error) { return f(ctx, in) }

func TestValidate_RejectsDeadlineAndRuleTogether(t *testing.T) {
	d := testDefinition()
	d.Nodes[1].DeadlineRule = "two-business-days" // review also has Deadline: 48h
	err := d.Validate()
	require.ErrorIs(t, err, ErrInvalidDefinition)
	assert.Contains(t, err.Error(), `node "review": must not declare both a deadline and a deadline rule`)

	d.Nodes[1].Deadline = 0
	require.NoError(t, d.Validate())
}

func TestRegister_RejectsRuleWithoutResolver(t *testing.T) {
	d := testDefinition()
	d.Nodes[1].Deadline = 0
	d.Nodes[1].DeadlineRule = "two-business-days"

	err := New(newFakeRepository(), Config{}, testLogger()).Register(d)
	require.ErrorIs(t, err, ErrUnknownDeadlineRule)
	assert.Contains(t, err.Error(), "two-business-days")

	resolver := resolverFunc(func(context.Context, DueInput) (time.Time, error) { return time.Time{}, nil })
	require.NoError(t, New(newFakeRepository(), Config{Deadlines: resolver}, testLogger()).Register(d))
}

func TestOpen_AppliesDeadlineRuleToStartToken(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	due := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	var calls []DueInput
	resolver := resolverFunc(func(_ context.Context, in DueInput) (time.Time, error) {
		calls = append(calls, in)
		return due, nil
	})
	d := testDefinition()
	d.Nodes[0].DeadlineRule = "two-business-days" // draft
	repo := newFakeRepository()
	e := New(repo, Config{Now: func() time.Time { return now }, Deadlines: resolver}, testLogger(), WithDefinitions(d))

	c := openTestCase(t, e)
	require.NotNil(t, c.DeadlineAt)
	assert.Equal(t, due, *c.DeadlineAt)
	tokens, err := e.Tokens(context.Background(), nil, c.ID)
	require.NoError(t, err)
	require.NotNil(t, tokens[0].DeadlineAt)
	assert.Equal(t, due, *tokens[0].DeadlineAt)

	require.Len(t, calls, 1)
	assert.Equal(t, "two-business-days", calls[0].Rule)
	assert.Equal(t, now, calls[0].From)
	assert.Equal(t, c.ID, calls[0].Case.ID)
	assert.Equal(t, "draft", calls[0].Token.Node)

	failing := resolverFunc(func(context.Context, DueInput) (time.Time, error) {
		return time.Time{}, errors.New("calendar unavailable")
	})
	e = New(newFakeRepository(), Config{Deadlines: failing}, testLogger(), WithDefinitions(d))
	_, err = e.Open(context.Background(), nil, OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "x", ActorID: "u1"})
	require.ErrorContains(t, err, "calendar unavailable", "a resolver error fails Open")
}

func TestMove_AppliesDeadlineRuleOnEntry(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	resolver := resolverFunc(func(_ context.Context, in DueInput) (time.Time, error) {
		return in.From.Add(24 * time.Hour), nil
	})
	d := testDefinition()
	d.Nodes[1].Deadline = 0
	d.Nodes[1].DeadlineRule = "next-day" // review
	repo := newFakeRepository()
	e := New(repo, Config{Now: func() time.Time { return now }, Deadlines: resolver}, testLogger(), WithDefinitions(d))
	c := openTestCase(t, e)

	c, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	require.NotNil(t, c.DeadlineAt)
	assert.Equal(t, now.Add(24*time.Hour), *c.DeadlineAt)

	failing := resolverFunc(func(context.Context, DueInput) (time.Time, error) { return time.Time{}, errors.New("no rule") })
	e = New(repo, Config{Deadlines: failing}, testLogger(), WithDefinitions(d))
	other := openInTenant(t, e, "", "other")
	before := storedEvents(t, repo, other.ID)
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: other.ID, Action: "submit", ActorID: "u1"})
	require.ErrorContains(t, err, "no rule")
	assert.Equal(t, before, storedEvents(t, repo, other.ID), "nothing is persisted")
}
