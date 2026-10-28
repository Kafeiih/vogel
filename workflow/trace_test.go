package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storedEvents returns every event the fake repository holds for caseID, in
// append order, bypassing the engine's read path.
func storedEvents(t *testing.T, repo *fakeRepository, caseID uuid.UUID) []Event {
	t.Helper()
	events, err := repo.ListEvents(context.Background(), nil, caseID)
	require.NoError(t, err)
	return events
}

func decodeAssignment(t *testing.T, e Event) AssignmentDetail {
	t.Helper()
	var d AssignmentDetail
	require.NoError(t, json.Unmarshal(e.Detail, &d), "detail %s", e.Detail)
	return d
}

func TestAppendEvent_WritesCurrentSchemaVersion(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	_, err = e.Claim(context.Background(), nil, c.ID, "approver-1")
	require.NoError(t, err)
	_, err = e.Release(context.Background(), nil, c.ID, "approver-1")
	require.NoError(t, err)
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "approver-1"})
	require.NoError(t, err)

	events := storedEvents(t, repo, c.ID)
	require.Len(t, events, 6)
	for _, ev := range events {
		assert.Equal(t, CurrentEventSchema, ev.SchemaVersion, "event seq=%d kind=%s", ev.Seq, ev.Kind)
	}
	assert.Equal(t, 2, CurrentEventSchema)
}

func TestListEvents_UnknownSchemaVersionFails(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())

	caseID := uuid.New()
	require.NoError(t, repo.Create(context.Background(), nil, &Case{ID: caseID, Domain: "d", ExternalID: "1"}))
	require.NoError(t, repo.AppendEvent(context.Background(), nil, &Event{
		ID: uuid.New(), CaseID: caseID, Seq: 1, Kind: EventOpened, SchemaVersion: CurrentEventSchema + 1,
	}))

	_, err := e.History(context.Background(), nil, caseID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnsupportedEventSchema), "got %v", err)
}

func TestListEvents_LegacyEventIsUpcast(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())

	caseID := uuid.New()
	require.NoError(t, repo.Create(context.Background(), nil, &Case{ID: caseID, Domain: "d", ExternalID: "1"}))
	// A repository that predates schema_version reports 0; one that stores
	// it reports 1 for rows written before this feature.
	require.NoError(t, repo.AppendEvent(context.Background(), nil, &Event{ID: uuid.New(), CaseID: caseID, Seq: 1, Kind: EventOpened}))
	require.NoError(t, repo.AppendEvent(context.Background(), nil, &Event{ID: uuid.New(), CaseID: caseID, Seq: 2, Kind: EventAssigned, SchemaVersion: 1}))

	events, err := e.History(context.Background(), nil, caseID)
	require.NoError(t, err)
	require.Len(t, events, 2)
	for _, ev := range events {
		assert.Equal(t, 1, ev.SchemaVersion, "seq=%d", ev.Seq)
		assert.JSONEq(t, `{}`, string(ev.Detail), "seq=%d", ev.Seq)
	}
}

func TestClaim_RecordsAssignmentFromTo(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	_, err := e.Claim(context.Background(), nil, c.ID, "buyer-1")
	require.NoError(t, err)

	events := storedEvents(t, repo, c.ID)
	last := events[len(events)-1]
	require.Equal(t, EventAssigned, last.Kind)
	assert.Equal(t, AssignmentDetail{From: "", To: "buyer-1"}, decodeAssignment(t, last))
}

// TestRelease_RecordsPreviousAssignee covers the From/To half of S5. The
// Reason half needs ReleaseInput (S2) and lands with T2 as
// TestRelease_RecordsPreviousAssigneeAndReason.
func TestRelease_RecordsPreviousAssignee(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	_, err := e.Claim(context.Background(), nil, c.ID, "buyer-1")
	require.NoError(t, err)
	_, err = e.Release(context.Background(), nil, c.ID, "supervisor-1")
	require.NoError(t, err)

	events := storedEvents(t, repo, c.ID)
	last := events[len(events)-1]
	require.Equal(t, EventUnassigned, last.Kind)
	assert.Equal(t, "supervisor-1", last.ActorID)
	assert.Equal(t, AssignmentDetail{From: "buyer-1", To: ""}, decodeAssignment(t, last))
}

func TestEventsWithoutDetail_CarryEmptyObject(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	for _, ev := range storedEvents(t, repo, c.ID) {
		assert.JSONEq(t, `{}`, string(ev.Detail), "kind=%s", ev.Kind)
	}
}

func TestOpenDetailed_ReturnsOpenedEvent(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	res, err := e.OpenDetailed(context.Background(), nil, OpenInput{
		Definition: "purchase", Domain: "compras", ExternalID: "solicitud-1", Unit: "finance", ActorID: "u1",
	})
	require.NoError(t, err)
	require.NotNil(t, res.Case)
	require.Len(t, res.Events, 1)
	assert.Equal(t, EventOpened, res.Events[0].Kind)
	assert.Equal(t, int64(1), res.Events[0].Seq)
	assert.Equal(t, int64(1), res.Case.LastSeq)
	assert.Equal(t, storedEvents(t, repo, res.Case.ID), res.Events)
}

func TestMoveDetailed_ReturnsAppendedEventsInSeqOrder(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(decisionToTerminalDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	res, err := e.MoveDetailed(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)

	// human hop, automatic route hop, automatic close.
	require.Len(t, res.Events, 3)
	assert.Equal(t, []EventKind{EventMoved, EventMoved, EventClosed},
		[]EventKind{res.Events[0].Kind, res.Events[1].Kind, res.Events[2].Kind})
	for i, ev := range res.Events {
		assert.Equal(t, int64(3+i), ev.Seq, "event %d", i)
	}
	assert.Equal(t, int64(5), res.Case.LastSeq)
	assert.Equal(t, StatusClosed, res.Case.Status)

	stored := storedEvents(t, repo, c.ID)
	assert.Equal(t, stored[2:], res.Events, "MoveDetailed must return exactly the events it appended")
}

func TestMove_AdvancesLastSeq(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openTestCase(t, e)
	assert.Equal(t, int64(1), c.LastSeq)

	moved, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), moved.LastSeq)

	claimed, err := e.Claim(context.Background(), nil, c.ID, "approver-1")
	require.NoError(t, err)
	assert.Equal(t, int64(3), claimed.LastSeq)

	released, err := e.Release(context.Background(), nil, c.ID, "approver-1")
	require.NoError(t, err)
	assert.Equal(t, int64(4), released.LastSeq)

	closed, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "approver-1"})
	require.NoError(t, err)
	assert.Equal(t, int64(6), closed.LastSeq, "approve appends moved + closed")

	stored, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(6), stored.LastSeq, "LastSeq must be persisted through Update")
	events := storedEvents(t, repo, c.ID)
	assert.Equal(t, stored.LastSeq, events[len(events)-1].Seq)
}

func TestNextSeq_DoesNotListEvents(t *testing.T) {
	repo := &listEventsCountingRepository{fakeRepository: newFakeRepository()}
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	_, err = e.Claim(context.Background(), nil, c.ID, "approver-1")
	require.NoError(t, err)
	_, err = e.Release(context.Background(), nil, c.ID, "approver-1")
	require.NoError(t, err)
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "approver-1"})
	require.NoError(t, err)

	assert.Equal(t, 0, repo.listEventsCalls, "assigning seq must use Case.LastSeq, never read the history")
}
