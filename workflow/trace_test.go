package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/reqctx"
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
	assert.Equal(t, 3, CurrentEventSchema)
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

func TestRelease_RecordsPreviousAssigneeAndReason(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	_, err := e.Claim(context.Background(), nil, c.ID, "buyer-1")
	require.NoError(t, err)
	released, err := e.ReleaseWith(context.Background(), nil, ReleaseInput{
		CaseID: c.ID, Actor: Actor{Kind: ActorOperator, ID: "supervisor-1"}, Reason: "on leave",
	})
	require.NoError(t, err)
	assert.Empty(t, released.AssignedTo)

	events := storedEvents(t, repo, c.ID)
	last := events[len(events)-1]
	require.Equal(t, EventUnassigned, last.Kind)
	assert.Equal(t, "supervisor-1", last.ActorID)
	assert.Equal(t, Actor{Kind: ActorOperator, ID: "supervisor-1"}, last.Actor)
	assert.Equal(t, AssignmentDetail{From: "buyer-1", To: "", Reason: "on leave"}, decodeAssignment(t, last))
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

func TestOpen_ActorIDAliasBecomesHumanActor(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e) // OpenInput.ActorID "u1", no Actor
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	history, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	require.Len(t, history, 2)
	for _, ev := range history {
		assert.Equal(t, Actor{Kind: ActorHuman, ID: "u1"}, ev.Actor, "kind=%s", ev.Kind)
		assert.Equal(t, "u1", ev.ActorID, "ActorID mirrors Actor.ID")
	}
}

func TestOpen_ActorAndEqualActorIDIsAccepted(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	res, err := e.OpenDetailed(context.Background(), nil, OpenInput{
		Definition: "purchase", Domain: "compras", ExternalID: "solicitud-1", Unit: "finance",
		ActorID: "u1", Actor: Actor{Kind: ActorAPIClient, ID: "u1", Via: "erp"},
	})
	require.NoError(t, err)
	assert.Equal(t, Actor{Kind: ActorAPIClient, ID: "u1", Via: "erp"}, res.Events[0].Actor)
}

func TestMove_ConflictingActorAndActorIDIsInvalid(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{
		CaseID: c.ID, Action: "submit", ActorID: "u1", Actor: Actor{Kind: ActorHuman, ID: "u2"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidActor), "got %v", err)

	stored, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "draft", stored.State, "a rejected actor must not move the case")
	assert.Len(t, storedEvents(t, repo, c.ID), 1)
}

func TestMove_SystemActorNeedsID(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", Actor: Actor{Kind: ActorSystem}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidActor), "got %v", err)
	assert.Len(t, storedEvents(t, repo, c.ID), 1)

	res, err := e.MoveDetailed(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", Actor: Actor{Kind: ActorSystem, ID: "deadline-sweeper"}})
	require.NoError(t, err)
	assert.Equal(t, Actor{Kind: ActorSystem, ID: "deadline-sweeper"}, res.Events[0].Actor)
	assert.Equal(t, "deadline-sweeper", res.Events[0].ActorID)
}

func TestMove_InvalidActorKindIsInvalid(t *testing.T) {
	for _, kind := range []ActorKind{"robot", ActorUnspecified} {
		t.Run(string(kind), func(t *testing.T) {
			repo := newFakeRepository()
			e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

			c := openTestCase(t, e)
			_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", Actor: Actor{Kind: kind, ID: "u1"}})
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidActor), "got %v", err)
			assert.Len(t, storedEvents(t, repo, c.ID), 1)
		})
	}
}

func TestListEvents_LegacyEventReadsUnspecifiedActor(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())

	caseID := uuid.New()
	require.NoError(t, repo.Create(context.Background(), nil, &Case{ID: caseID, Domain: "d", ExternalID: "1"}))
	require.NoError(t, repo.AppendEvent(context.Background(), nil, &Event{
		ID: uuid.New(), CaseID: caseID, Seq: 1, Kind: EventOpened, ActorID: "u1", SchemaVersion: 1,
	}))

	events, err := e.History(context.Background(), nil, caseID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, Actor{Kind: ActorUnspecified, ID: "u1"}, events[0].Actor)
	assert.Equal(t, "u1", events[0].ActorID)
}

func TestClaim_WrapperRecordsHumanActor(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	claimed, err := e.Claim(context.Background(), nil, c.ID, "buyer-1")
	require.NoError(t, err)
	assert.Equal(t, "buyer-1", claimed.AssignedTo)
	_, err = e.Release(context.Background(), nil, c.ID, "buyer-1")
	require.NoError(t, err)

	events := storedEvents(t, repo, c.ID)
	require.Len(t, events, 3)
	for _, ev := range events[1:] {
		assert.Equal(t, Actor{Kind: ActorHuman, ID: "buyer-1"}, ev.Actor, "kind=%s", ev.Kind)
	}
	assert.Equal(t, AssignmentDetail{From: "buyer-1"}, decodeAssignment(t, events[2]), "Release records no reason")
}

func TestClaimWith_SameActorIsNoOp(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	actor := Actor{Kind: ActorOperator, ID: "op-1", ActingAs: "jefe@finanzas"}
	_, err := e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, Actor: actor})
	require.NoError(t, err)
	again, err := e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, Actor: actor})
	require.NoError(t, err)
	assert.Equal(t, "op-1", again.AssignedTo)
	assert.Len(t, storedEvents(t, repo, c.ID), 2, "re-claim by the same actor appends nothing")

	_, err = e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, Actor: Actor{Kind: ActorSystem}})
	assert.True(t, errors.Is(err, ErrInvalidActor), "got %v", err)
}

func openInTenant(t *testing.T, e *Engine, tenant, externalID string) *Case {
	t.Helper()
	c, err := e.Open(context.Background(), nil, OpenInput{
		Definition: "purchase", Tenant: tenant, Domain: "compras", ExternalID: externalID, Unit: "finance", ActorID: "u1",
	})
	require.NoError(t, err)
	return c
}

func caseIDs(cases []Case) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(cases))
	for _, c := range cases {
		ids = append(ids, c.ID)
	}
	return ids
}

// workItemCaseIDs returns the case ID of every work item, in order.
func workItemCaseIDs(items []WorkItem) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.Case.ID)
	}
	return ids
}

func TestInbox_FiltersByTenant(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	a := openInTenant(t, e, "tenant-a", "solicitud-1")
	b := openInTenant(t, e, "tenant-b", "solicitud-1")
	assert.Equal(t, "tenant-a", a.Tenant)

	got, err := e.Inbox(context.Background(), nil, InboxFilter{Tenant: "tenant-a", Domain: "compras"})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{a.ID}, workItemCaseIDs(got))

	got, err = e.Inbox(context.Background(), nil, InboxFilter{Tenant: "tenant-b"})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{b.ID}, workItemCaseIDs(got))
}

func TestInbox_EmptyTenantMatchesAnyTenant(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	a := openInTenant(t, e, "tenant-a", "solicitud-1")
	none := openInTenant(t, e, "", "solicitud-2")

	got, err := e.Inbox(context.Background(), nil, InboxFilter{Domain: "compras"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{a.ID, none.ID}, workItemCaseIDs(got))
}

func TestEvent_CopiesCaseTenant(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openInTenant(t, e, "tenant-a", "solicitud-1")
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
		assert.Equal(t, "tenant-a", ev.Tenant, "seq=%d kind=%s", ev.Seq, ev.Kind)
	}
}

func TestMove_StampsRequestIDFromContext(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	ctx := reqctx.WithRequestID(context.Background(), "req-42")
	c, err := e.Open(ctx, nil, OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "1", Unit: "finance", ActorID: "u1"})
	require.NoError(t, err)
	_, err = e.Move(ctx, nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	_, err = e.Claim(reqctx.WithRequestID(context.Background(), "req-43"), nil, c.ID, "approver-1")
	require.NoError(t, err)

	events := storedEvents(t, repo, c.ID)
	require.Len(t, events, 3)
	assert.Equal(t, "req-42", events[0].RequestID, "opened")
	assert.Equal(t, "req-42", events[1].RequestID, "moved")
	assert.Equal(t, "req-43", events[2].RequestID, "assigned")
}

func TestMove_StampsTraceIDsFromPort(t *testing.T) {
	repo := newFakeRepository()
	type spanKey struct{}
	traceIDs := func(ctx context.Context) (string, string) {
		if span, ok := ctx.Value(spanKey{}).(string); ok {
			return "trace-1", span
		}
		return "", ""
	}
	e := New(repo, Config{TraceIDs: traceIDs}, testLogger(), WithDefinitions(decisionToTerminalDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	res, err := e.MoveDetailed(context.WithValue(context.Background(), spanKey{}, "span-7"), nil,
		MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)

	require.Len(t, res.Events, 3, "human hop, route hop, close")
	for _, ev := range res.Events {
		assert.Equal(t, "trace-1", ev.TraceID, "seq %d", ev.Seq)
		assert.Equal(t, "span-7", ev.SpanID, "seq %d", ev.Seq)
	}
	events := storedEvents(t, repo, c.ID)
	assert.Equal(t, res.Events, events[2:], "the port's ids must be persisted, not only returned")
	assert.Empty(t, events[0].TraceID, "a context without a span yields empty ids")
}

func TestMove_NilTraceIDsPortLeavesEmpty(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	for _, ev := range storedEvents(t, repo, c.ID) {
		assert.Empty(t, ev.TraceID, "seq %d", ev.Seq)
		assert.Empty(t, ev.SpanID, "seq %d", ev.Seq)
		assert.Empty(t, ev.RequestID, "seq %d", ev.Seq)
	}
}

// TestOpen_ActorIDAliasKeepsExplicitKind pins that the ActorID alias only
// supplies the ID: an explicitly set Kind is kept, and ActorHuman applies
// only when Kind is empty.
func TestOpen_ActorIDAliasKeepsExplicitKind(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c, err := e.Open(context.Background(), nil, OpenInput{
		Definition: "purchase", Domain: "compras", ExternalID: "1", Unit: "finance",
		ActorID: "bob", Actor: Actor{Kind: ActorOperator},
	})
	require.NoError(t, err)

	events := storedEvents(t, repo, c.ID)
	require.Len(t, events, 1)
	assert.Equal(t, Actor{Kind: ActorOperator, ID: "bob"}, events[0].Actor)
}

func TestClaimWith_EmptyActorIDIsInvalid(t *testing.T) {
	for _, kind := range []ActorKind{"", ActorHuman, ActorOperator, ActorAPIClient, ActorAnonymous} {
		t.Run(string(kind), func(t *testing.T) {
			repo := newFakeRepository()
			e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
			c := openTestCase(t, e)
			before := storedEvents(t, repo, c.ID)

			_, err := e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, Actor: Actor{Kind: kind}})
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidActor), "got %v", err)

			stored, err := repo.GetByID(context.Background(), nil, c.ID)
			require.NoError(t, err)
			assert.Equal(t, *c, *stored, "a rejected claim must not touch the case")
			assert.Equal(t, before, storedEvents(t, repo, c.ID), "a rejected claim must append nothing")
		})
	}
}

func TestClaim_EmptyActorIDIsInvalid(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	c := openTestCase(t, e)

	_, err := e.Claim(context.Background(), nil, c.ID, "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidActor), "got %v", err)
	assert.Len(t, storedEvents(t, repo, c.ID), 1, "only the opened event")
}

func TestReleaseWith_EmptyActorIDIsStillAllowed(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	c := openTestCase(t, e)
	_, err := e.Claim(context.Background(), nil, c.ID, "buyer-1")
	require.NoError(t, err)

	released, err := e.ReleaseWith(context.Background(), nil, ReleaseInput{CaseID: c.ID, Actor: Actor{Kind: ActorAnonymous}, Reason: "abandoned"})
	require.NoError(t, err)
	assert.Empty(t, released.AssignedTo)
}

func TestMove_NonHumanActorRecordedOnEveryHop(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(decisionToTerminalDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	actor := Actor{Kind: ActorAPIClient, ID: "client-7", ActingAs: "approver@finance:delegate", OnBehalfOf: "u9", Via: "erp"}
	res, err := e.MoveDetailed(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", Actor: actor})
	require.NoError(t, err)

	require.Len(t, res.Events, 3, "human hop, route hop, close")
	stored := storedEvents(t, repo, c.ID)
	for i, ev := range stored[2:] {
		assert.Equal(t, actor, ev.Actor, "event %d kind=%s", i, ev.Kind)
		assert.Equal(t, "client-7", ev.ActorID, "event %d kind=%s", i, ev.Kind)
	}
}

func TestReleaseWith_SystemActorNeedsID(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	c := openTestCase(t, e)
	claimed, err := e.Claim(context.Background(), nil, c.ID, "buyer-1")
	require.NoError(t, err)
	before := storedEvents(t, repo, c.ID)

	_, err = e.ReleaseWith(context.Background(), nil, ReleaseInput{CaseID: c.ID, Actor: Actor{Kind: ActorSystem}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidActor), "got %v", err)

	stored, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, *claimed, *stored, "a rejected release must not touch the case")
	assert.Equal(t, before, storedEvents(t, repo, c.ID), "a rejected release must append nothing")
}
