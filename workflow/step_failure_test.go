package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

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
// failure takes the next seq without touching revision, so Move's
// revision CAS still matches, and the seq collision on append surfaces as
// ErrConflict, which the caller retries from a fresh read. The fake has no
// transaction to roll back, so the retry is covered by the integration test.
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

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.ErrorIs(t, err, ErrConflict)
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
