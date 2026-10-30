package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/pgxtx"
)

func TestMove_WithStepAppendsStepSubmittedBeforeMoved(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(decisionToTerminalDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	actor := Actor{Kind: ActorHuman, ID: "u2", ActingAs: "approver@finance"}
	step := &StepRecord{StepID: "review-form", FormVersion: 3, PayloadDigest: "sha256:abc"}
	res, err := e.MoveDetailed(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", Actor: actor, Step: step})
	require.NoError(t, err)

	// step_submitted, human hop, route hop, close: one contiguous seq block.
	require.Len(t, res.Events, 4)
	assert.Equal(t, []EventKind{EventStepSubmitted, EventMoved, EventMoved, EventClosed},
		[]EventKind{res.Events[0].Kind, res.Events[1].Kind, res.Events[2].Kind, res.Events[3].Kind})
	for i, ev := range res.Events {
		assert.Equal(t, int64(3+i), ev.Seq, "event %d", i)
		assert.Equal(t, actor, ev.Actor, "event %d", i)
	}
	sub := res.Events[0]
	assert.Equal(t, "review", sub.FromState, "the step is submitted from the state the case was in")
	assert.Equal(t, "review", sub.ToState)
	assert.Equal(t, "approve", sub.Action)
	assert.Equal(t, int64(6), res.Case.LastSeq)
	assert.Equal(t, storedEvents(t, repo, c.ID)[2:], res.Events)
}

func TestMove_StepDetailHoldsDigestOnly(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{
		CaseID: c.ID, Action: "submit", ActorID: "u1",
		Step: &StepRecord{StepID: "draft-form", FormVersion: 1, PayloadDigest: "sha256:0f1e"},
	})
	require.NoError(t, err)

	sub := storedEvents(t, repo, c.ID)[1]
	require.Equal(t, EventStepSubmitted, sub.Kind)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(sub.Detail, &fields))
	assert.Equal(t, map[string]any{"step_id": "draft-form", "form_version": float64(1), "payload_digest": "sha256:0f1e"}, fields,
		"the detail is the step reference and digest, never the payload")

	var d StepDetail
	require.NoError(t, json.Unmarshal(sub.Detail, &d))
	assert.Equal(t, StepDetail{StepID: "draft-form", FormVersion: 1, PayloadDigest: "sha256:0f1e"}, d)
}

func TestMove_WithoutStepAppendsNoStepEvent(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	events := storedEvents(t, repo, c.ID)
	require.Len(t, events, 2)
	assert.Equal(t, EventMoved, events[1].Kind)
}

func TestMove_RejectedWithStepAppendsNothing(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	before := storedEvents(t, repo, c.ID)

	_, err = e.Move(context.Background(), nil, MoveInput{
		CaseID: c.ID, Action: "approve", ActorID: "u2",
		Step: &StepRecord{StepID: "review-form", FormVersion: 1, PayloadDigest: "sha256:1"},
	})
	require.ErrorIs(t, err, ErrGuardRejected)
	assert.Equal(t, before, storedEvents(t, repo, c.ID), "a rejected move records no step submission")
}

func TestRecordFailure_AppendsWithoutChangingStateOrRevision(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	before, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)

	actor := Actor{Kind: ActorHuman, ID: "u2"}
	evt, err := e.RecordFailure(context.Background(), nil, FailureInput{
		CaseID: c.ID, Actor: actor, Action: "approve", Kind: FailureGuardRejected,
		Detail: json.RawMessage(`{"guard":"under-budget"}`),
	})
	require.NoError(t, err)

	assert.Equal(t, EventFailed, evt.Kind)
	assert.Equal(t, int64(2), evt.Seq)
	assert.Equal(t, "draft", evt.FromState)
	assert.Equal(t, "draft", evt.ToState)
	assert.Equal(t, "approve", evt.Action)
	assert.Equal(t, actor, evt.Actor)
	var d FailureDetail
	require.NoError(t, json.Unmarshal(evt.Detail, &d))
	assert.Equal(t, FailureGuardRejected, d.Kind)
	assert.JSONEq(t, `{"guard":"under-budget"}`, string(d.Detail))

	after, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, before.Revision, after.Revision, "a recorded failure never advances revision")
	assert.Equal(t, before.State, after.State)
	assert.Equal(t, before.AssignedTo, after.AssignedTo)
	assert.Equal(t, int64(2), after.LastSeq, "the failure takes the next seq")

	events := storedEvents(t, repo, c.ID)
	require.Len(t, events, 2)
	assert.Equal(t, evt, events[1])

	// The case still moves normally afterwards, continuing the seq.
	moved, err := e.MoveDetailed(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), moved.Events[0].Seq)
}

func TestRecordFailure_NoRouteKind(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	evt, err := e.RecordFailure(context.Background(), nil, FailureInput{CaseID: c.ID, Actor: Actor{Kind: ActorSystem, ID: "router"}, Action: "submit", Kind: FailureNoRoute})
	require.NoError(t, err)
	assert.Equal(t, EventNoRoute, evt.Kind)
	var d FailureDetail
	require.NoError(t, json.Unmarshal(evt.Detail, &d))
	assert.Equal(t, FailureNoRoute, d.Kind)
}

func TestRecordFailure_RejectsInvalidInput(t *testing.T) {
	cases := map[string]FailureInput{
		"unknown kind":       {Kind: "exploded"},
		"empty kind":         {},
		"detail not json":    {Kind: FailureConflict, Detail: json.RawMessage(`{nope`)},
		"detail not object":  {Kind: FailureConflict, Detail: json.RawMessage(`[1]`)},
		"invalid actor kind": {Kind: FailureConflict, Actor: Actor{Kind: "robot", ID: "r"}},
		"system actor no id": {Kind: FailureConflict, Actor: Actor{Kind: ActorSystem}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			repo := newFakeRepository()
			e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
			c := openTestCase(t, e)
			before, err := repo.GetByID(context.Background(), nil, c.ID)
			require.NoError(t, err)

			in.CaseID = c.ID
			_, err = e.RecordFailure(context.Background(), nil, in)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidFailure) || errors.Is(err, ErrInvalidActor), "got %v", err)

			after, err := repo.GetByID(context.Background(), nil, c.ID)
			require.NoError(t, err)
			assert.Equal(t, *before, *after, "an invalid failure must not reserve a seq")
			assert.Len(t, storedEvents(t, repo, c.ID), 1)
		})
	}
}

func TestRecordFailure_UnknownCaseFails(t *testing.T) {
	e := New(newFakeRepository(), Config{}, testLogger(), WithDefinitions(testDefinition()))
	_, err := e.RecordFailure(context.Background(), nil, FailureInput{CaseID: [16]byte{1}, Kind: FailureConflict})
	require.ErrorIs(t, err, ErrCaseNotFound)
}

// TestMove_ConcurrentRecordFailureIsAConflict pins what happens when a
// failure is recorded on the case between Move's read and its write: the
// failure takes the next seq without touching revision, and Move's
// compare-and-swap also checks the LastSeq it read, so Move fails with
// ErrConflict before writing anything, with or without a transaction, and a
// retry from a fresh read continues after the failure.
func TestMove_ConcurrentRecordFailureIsAConflict(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	c := openTestCase(t, e)

	var recorded bool
	require.NoError(t, e.RegisterGuard("under-budget", func(ctx context.Context, db pgxtx.DBTX, gc Case) (bool, error) {
		if !recorded {
			recorded = true
			if _, err := e.RecordFailure(ctx, db, FailureInput{CaseID: gc.ID, Kind: FailureConflict}); err != nil {
				return false, fmt.Errorf("record failure: %w", err)
			}
		}
		return true, nil
	}))
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	before, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.ErrorIs(t, err, ErrConflict)

	after, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "review", after.State, "the conflicting Move must not write the case")
	assert.Equal(t, before.Revision, after.Revision)
	assert.Equal(t, int64(3), after.LastSeq, "only the failure's seq was taken")
	assert.Len(t, storedEvents(t, repo, c.ID), 3, "opened, moved, failed")

	res, err := e.MoveDetailed(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err, "a retry from a fresh read succeeds")
	assert.Equal(t, int64(4), res.Events[0].Seq)
}

// TestClaimAndRelease_ConcurrentRecordFailureIsAConflict drives ClaimWith
// and ReleaseWith through a RecordFailure committed between their read and
// their Update: both fail with ErrConflict and leave the case and its
// history exactly as the failure left them.
func TestClaimAndRelease_ConcurrentRecordFailureIsAConflict(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, e *Engine, c *Case)
		act     func(e *Engine, c *Case) (*Case, error)
	}{
		{
			name:    "claim",
			prepare: func(*testing.T, *Engine, *Case) {},
			act: func(e *Engine, c *Case) (*Case, error) {
				return e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, Actor: Actor{Kind: ActorHuman, ID: "u2"}})
			},
		},
		{
			name: "release",
			prepare: func(t *testing.T, e *Engine, c *Case) {
				_, err := e.Claim(context.Background(), nil, c.ID, "u2")
				require.NoError(t, err)
			},
			act: func(e *Engine, c *Case) (*Case, error) {
				return e.ReleaseWith(context.Background(), nil, ReleaseInput{CaseID: c.ID, Actor: Actor{Kind: ActorHuman, ID: "u2"}, Reason: "busy"})
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inner := newFakeRepository()
			direct := New(inner, Config{}, testLogger(), WithDefinitions(testDefinition()))
			c := openTestCase(t, direct)
			tc.prepare(t, direct, c)

			repo := &racingRepository{fakeRepository: inner, beforeUpdate: func() {
				_, err := direct.RecordFailure(context.Background(), nil, FailureInput{CaseID: c.ID, Kind: FailureGuardRejected})
				require.NoError(t, err)
			}}
			e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

			_, err := tc.act(e, c)
			require.ErrorIs(t, err, ErrConflict)

			after, err := inner.GetByID(context.Background(), nil, c.ID)
			require.NoError(t, err)
			events := storedEvents(t, inner, c.ID)
			last := events[len(events)-1]
			assert.Equal(t, EventFailed, last.Kind, "the failure is the newest event; nothing was appended after it")
			assert.Equal(t, last.Seq, after.LastSeq)

			// A retry from a fresh read goes through.
			_, err = tc.act(e, c)
			require.NoError(t, err)
		})
	}
}

func TestFailureKindOf_MapsEverySentinel(t *testing.T) {
	cases := []struct {
		err  error
		want FailureKind
	}{
		{ErrNoRoute, FailureNoRoute},
		{ErrGuardRejected, FailureGuardRejected},
		{ErrConflict, FailureConflict},
		{ErrInvalidActor, FailureValidationFailed},
		{ErrCommentRequired, FailureCommentRejected},
		{ErrCommentNotAllowed, FailureCommentRejected},
		{ErrReturnNotVisited, FailureReturnNotVisited},
		{ErrInvalidTransition, FailureTransitionNotFound},
	}
	for _, tc := range cases {
		wrapped := fmt.Errorf("workflow: move case x: %w", tc.err)
		got, ok := FailureKindOf(wrapped)
		assert.True(t, ok, "%v", tc.err)
		assert.Equal(t, tc.want, got, "%v", tc.err)
		assert.True(t, got.Valid(), "%v", tc.err)
	}

	for _, err := range []error{nil, errors.New("boom"), ErrCaseClosed, ErrCaseNotFound, ErrAlreadyAssigned} {
		_, ok := FailureKindOf(err)
		assert.False(t, ok, "%v is not a rejected move", err)
	}
}

func TestParticipated_ReturnsCasesWhereActorActedAfterReassign(t *testing.T) {
	repo := newFakeRepository()
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	e := New(repo, Config{Now: func() time.Time { clock = clock.Add(time.Minute); return clock }}, testLogger(), WithDefinitions(testDefinition()))
	ctx := context.Background()
	open := func(tenant, ext, actor string) *Case {
		c, err := e.Open(ctx, nil, OpenInput{Definition: "purchase", Tenant: tenant, Domain: "compras", ExternalID: ext, Unit: "finance", Actor: Actor{Kind: ActorHuman, ID: actor}})
		require.NoError(t, err)
		return c
	}

	first := open("t1", "1", "u1")  // u1 opened it, nothing else
	second := open("t1", "2", "u9") // u1 claims, releases, someone else takes it
	_, err := e.Claim(ctx, nil, second.ID, "u1")
	require.NoError(t, err)
	_, err = e.Release(ctx, nil, second.ID, "u1")
	require.NoError(t, err)
	_, err = e.Claim(ctx, nil, second.ID, "u9")
	require.NoError(t, err)
	open("t1", "3", "u9")                // u1 never acted
	otherTenant := open("t2", "4", "u1") // u1 acted in another tenant
	// A closed case still counts: u1 closes the first one, which also makes
	// it u1's newest activity in t1.
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))
	for _, action := range []string{"submit", "approve"} {
		_, err = e.Move(ctx, nil, MoveInput{CaseID: first.ID, Action: action, Actor: Actor{Kind: ActorHuman, ID: "u1"}})
		require.NoError(t, err)
	}

	got, err := e.Participated(ctx, nil, ParticipationFilter{Tenant: "t1", ActorID: "u1"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, first.ID, got[0].ID, "newest activity of the actor first")
	assert.Equal(t, StatusClosed, got[0].Status, "closed cases are listed")
	assert.Equal(t, second.ID, got[1].ID)
	assert.Equal(t, "u9", got[1].AssignedTo, "the case is listed although it is assigned to someone else now")

	all, err := e.Participated(ctx, nil, ParticipationFilter{ActorID: "u1"})
	require.NoError(t, err)
	require.Len(t, all, 3, "an empty tenant means any tenant")
	assert.Equal(t, []uuid.UUID{first.ID, otherTenant.ID, second.ID}, []uuid.UUID{all[0].ID, all[1].ID, all[2].ID})

	page, err := e.Participated(ctx, nil, ParticipationFilter{ActorID: "u1", Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, otherTenant.ID, page[0].ID)
}

func TestParticipated_EqualTimestampsOrderByCaseID(t *testing.T) {
	repo := newFakeRepository()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	e := New(repo, Config{Now: func() time.Time { return at }}, testLogger(), WithDefinitions(testDefinition()))
	ctx := context.Background()

	const cases = 5
	want := make([]uuid.UUID, 0, cases)
	for i := range cases {
		c, err := e.Open(ctx, nil, OpenInput{Definition: "purchase", Domain: "compras", ExternalID: fmt.Sprint(i), Unit: "finance", Actor: Actor{Kind: ActorHuman, ID: "u1"}})
		require.NoError(t, err)
		want = append(want, c.ID)
	}
	slices.SortFunc(want, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })

	got, err := e.Participated(ctx, nil, ParticipationFilter{ActorID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, want, caseIDs(got), "cases whose newest activity shares a timestamp are ordered by case id")

	paged := make([]uuid.UUID, 0, len(want))
	for offset := range len(want) {
		page, err := e.Participated(ctx, nil, ParticipationFilter{ActorID: "u1", Limit: 1, Offset: offset})
		require.NoError(t, err)
		paged = append(paged, caseIDs(page)...)
	}
	assert.Equal(t, want, paged, "paging one at a time neither repeats nor skips a case")
}

func TestParticipated_RejectsOffsetOutOfRange(t *testing.T) {
	e := New(newFakeRepository(), Config{}, testLogger(), WithDefinitions(testDefinition()))
	for _, offset := range []int{-1, MaxListOffset + 1} {
		_, err := e.Participated(context.Background(), nil, ParticipationFilter{ActorID: "u1", Offset: offset})
		require.ErrorIs(t, err, ErrOffsetOutOfRange, "offset %d", offset)
	}
}

func TestParticipated_RequiresActorID(t *testing.T) {
	e := New(newFakeRepository(), Config{}, testLogger(), WithDefinitions(testDefinition()))
	_, err := e.Participated(context.Background(), nil, ParticipationFilter{Tenant: "t1"})
	require.ErrorIs(t, err, ErrInvalidActor)
}

func TestParticipationFilter_PageFollowsInboxRules(t *testing.T) {
	for _, tc := range []struct{ limit, offset int }{{0, 0}, {-5, 10}, {MaxListLimit + 1, 3}, {50, MaxListOffset}} {
		wantL, wantO, wantErr := InboxFilter{Limit: tc.limit, Offset: tc.offset}.Page()
		gotL, gotO, gotErr := ParticipationFilter{Limit: tc.limit, Offset: tc.offset}.Page()
		assert.Equal(t, wantL, gotL, "%+v", tc)
		assert.Equal(t, wantO, gotO, "%+v", tc)
		assert.Equal(t, wantErr, gotErr, "%+v", tc)
	}
}
