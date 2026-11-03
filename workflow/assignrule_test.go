package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/pgxtx"
)

// stubAssignees answers every rule through answer and records its inputs.
type stubAssignees struct {
	answer func(in AssignInput) (string, error)
	calls  []AssignInput
}

func (s *stubAssignees) Assignee(_ context.Context, _ pgxtx.DBTX, in AssignInput) (string, error) {
	s.calls = append(s.calls, in)
	return s.answer(in)
}

// reviewDefinition is draft -> review -> done, with a way back from review to
// draft, and "review" and "draft" assigned by rules.
func reviewDefinition() Definition {
	return Definition{
		Name: "review", Version: 1,
		Nodes: []Node{
			{ID: "draft", Start: true, AssignRule: "assign_from:draft"},
			{ID: "review", AssignRule: "assign_from:draft"},
			{ID: "plain"},
			{ID: "done", Terminal: true},
		},
		Transitions: []Transition{
			{From: "draft", To: "review", Action: "send"},
			{From: "draft", To: "plain", Action: "plain"},
			{From: "plain", To: "done", Action: "finish"},
			{From: "review", To: "draft", Action: "send-back", Return: true},
			{From: "review", To: "done", Action: "finish"},
		},
	}
}

func assignEngine(t *testing.T, a AssigneeResolver, defs ...Definition) (*Engine, *fakeRepository) {
	t.Helper()
	repo := newFakeRepository()
	e := New(repo, Config{Assignees: a}, testLogger())
	for _, d := range defs {
		require.NoError(t, e.Register(d))
	}
	return e, repo
}

func TestValidate_AssignRuleOnlyOnTask(t *testing.T) {
	for name, node := range map[string]Node{
		"decision": {ID: "x", Kind: NodeDecision, AssignRule: "r"},
		"split":    {ID: "x", Kind: NodeParallel, Join: "merge", AssignRule: "r"},
		"join":     {ID: "x", Kind: NodeJoin, AssignRule: "r"},
		"terminal": {ID: "x", Terminal: true, AssignRule: "r"},
	} {
		t.Run(name, func(t *testing.T) {
			d := parallelDefinition()
			d.Nodes = append(d.Nodes, node)
			err := d.Validate()
			require.ErrorIs(t, err, ErrInvalidDefinition)
			assert.Contains(t, err.Error(), `"x": must not declare an assign rule`)
		})
	}
	assert.NoError(t, reviewDefinition().Validate())
}

func TestRegister_RejectsAssignRuleWithoutResolver(t *testing.T) {
	e := New(newFakeRepository(), Config{}, testLogger())
	err := e.Register(reviewDefinition())
	require.ErrorIs(t, err, ErrUnknownAssignRule)
	assert.Contains(t, err.Error(), "assign_from:draft")

	withResolver := New(newFakeRepository(), Config{Assignees: &stubAssignees{}}, testLogger())
	assert.NoError(t, withResolver.Register(reviewDefinition()))
}

func TestAssignRule_EmptyAnswerStaysInPool(t *testing.T) {
	a := &stubAssignees{answer: func(AssignInput) (string, error) { return "", nil }}
	e, repo := assignEngine(t, a, reviewDefinition())
	c := openCase(t, e, "review")
	res := move(t, e, c.ID, uuid.Nil, "send")
	assertInvariants(t, e, repo, c.ID)

	assert.Equal(t, []EventKind{EventMoved}, eventKinds(res.Events), "no assignment event")
	assert.Empty(t, res.Case.AssignedTo)
	require.Len(t, a.calls, 1)
}

func TestAssignRule_AnswerAppendsAssignedEvent(t *testing.T) {
	a := &stubAssignees{answer: func(AssignInput) (string, error) { return "ana", nil }}
	e, repo := assignEngine(t, a, reviewDefinition())
	c := openCase(t, e, "review")
	res := move(t, e, c.ID, uuid.Nil, "send")
	assertInvariants(t, e, repo, c.ID)

	require.Equal(t, []EventKind{EventMoved, EventAssigned}, eventKinds(res.Events))
	assigned := res.Events[1]
	var detail AssignmentDetail
	require.NoError(t, json.Unmarshal(assigned.Detail, &detail))
	assert.Equal(t, AssignmentDetail{To: "ana", Reason: "assign_rule:assign_from:draft"}, detail)
	assert.Equal(t, "review", assigned.ToState)
	assert.Equal(t, "ana", res.Case.AssignedTo)

	in := a.calls[0]
	assert.Equal(t, "assign_from:draft", in.Rule)
	assert.Equal(t, "review", in.Node.ID)
	assert.Equal(t, c.ID, in.Case.ID)
	assert.Equal(t, res.Events[0].TokenID, in.Token.ID)
	assert.Equal(t, "u1", LastActorOf(in.History, "draft"), "History includes the hop this call just planned")
}

func TestAssignRule_ErrorPersistsNothing(t *testing.T) {
	boom := errors.New("directory down")
	a := &stubAssignees{answer: func(AssignInput) (string, error) { return "", boom }}
	e, repo := assignEngine(t, a, reviewDefinition())
	c := openCase(t, e, "review")

	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "send", ActorID: "u1"})
	require.ErrorIs(t, err, boom)
	stored, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "draft", stored.State)
	assert.Equal(t, c.LastSeq, stored.LastSeq, "nothing is persisted")
}

// TestAssignRule_RunsOnMoveJoinReturnAndExpire covers the paths into a node.
func TestAssignRule_RunsOnMoveJoinReturnAndExpire(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	answer := func(in AssignInput) (string, error) { return "owner-of-" + in.Node.ID, nil }

	t.Run("move and return", func(t *testing.T) {
		a := &stubAssignees{answer: answer}
		e, repo := assignEngine(t, a, reviewDefinition())
		c := openCase(t, e, "review")
		assert.Empty(t, a.calls, "Open applies no rule, so a start node stays as it is")

		res := move(t, e, c.ID, uuid.Nil, "send")
		assert.Equal(t, "owner-of-review", res.Case.AssignedTo)
		res = move(t, e, c.ID, uuid.Nil, "send-back")
		assertInvariants(t, e, repo, c.ID)
		assert.Equal(t, "owner-of-draft", res.Case.AssignedTo, "a return transition takes the rule too")
	})
	t.Run("split and join", func(t *testing.T) {
		d := parallelDefinition()
		for i := range d.Nodes {
			if d.Nodes[i].ID == "legal" || d.Nodes[i].ID == "signoff" {
				d.Nodes[i].AssignRule = "by-node"
			}
		}
		a := &stubAssignees{answer: answer}
		e, repo := assignEngine(t, a, d)
		c := openCase(t, e, "contract")
		move(t, e, c.ID, uuid.Nil, "submit")
		active := activeTokens(t, e, c.ID)
		assert.Equal(t, "owner-of-legal", active["legal"].AssignedTo, "a split's branch")
		assert.Empty(t, active["finance"].AssignedTo, "a node without a rule stays in the pool")

		move(t, e, c.ID, active["legal"].ID, "clear")
		res := move(t, e, c.ID, active["finance"].ID, "clear")
		assertInvariants(t, e, repo, c.ID)
		assert.Equal(t, "owner-of-signoff", res.Case.AssignedTo, "the token leaving a join")
	})
	t.Run("expire", func(t *testing.T) {
		d := Definition{
			Name: "overdue", Version: 1,
			Nodes: []Node{
				{ID: "draft", Start: true},
				{ID: "first", Deadline: time.Hour, OnExpire: "escalate"},
				{ID: "second", AssignRule: "by-node"},
				{ID: "done", Terminal: true},
			},
			Transitions: []Transition{
				{From: "draft", To: "first", Action: "send"},
				{From: "first", To: "second", Action: "escalate"},
				{From: "second", To: "done", Action: "finish"},
			},
		}
		a := &stubAssignees{answer: answer}
		repo := newFakeRepository()
		e := New(repo, Config{Assignees: a, Now: func() time.Time { return now }}, testLogger())
		require.NoError(t, e.Register(d))
		c := openCase(t, e, "overdue")
		move(t, e, c.ID, uuid.Nil, "send")

		now = now.Add(2 * time.Hour)
		res, err := e.Expire(context.Background(), nil, ExpireInput{CaseID: c.ID, Actor: Actor{Kind: ActorSystem, ID: "scheduler"}})
		require.NoError(t, err)
		assertInvariants(t, e, repo, c.ID)
		assert.Equal(t, "owner-of-second", res.Case.AssignedTo)
	})
}

func TestAssignRule_UnsetClearsAsToday(t *testing.T) {
	a := &stubAssignees{answer: func(AssignInput) (string, error) { return "x", nil }}
	e, repo := assignEngine(t, a, reviewDefinition())
	c := openCase(t, e, "review")
	_, err := e.Claim(context.Background(), nil, c.ID, "ana")
	require.NoError(t, err)

	res := move(t, e, c.ID, uuid.Nil, "plain")
	assertInvariants(t, e, repo, c.ID)
	assert.Empty(t, res.Case.AssignedTo, "entering a node without a rule clears the assignee")
	assert.Equal(t, []EventKind{EventMoved}, eventKinds(res.Events))
	assert.Empty(t, a.calls, "and asks nobody")
}

// TestAssignRule_HistorySeesTheNewerActorOnRevisit is the send-back case: Ana
// sends, the case returns, Beto sends again. The rule must see Beto's move, which
// this call only planned, over Ana's stored one.
func TestAssignRule_HistorySeesTheNewerActorOnRevisit(t *testing.T) {
	a := &stubAssignees{answer: func(in AssignInput) (string, error) { return LastActorOf(in.History, "draft"), nil }}
	e, repo := assignEngine(t, a, reviewDefinition())
	c := openCase(t, e, "review")
	ctx := context.Background()

	moved, err := e.Move(ctx, nil, MoveInput{CaseID: c.ID, Action: "send", ActorID: "ana"})
	require.NoError(t, err)
	assert.Equal(t, "ana", moved.AssignedTo)
	_, err = e.Move(ctx, nil, MoveInput{CaseID: c.ID, Action: "send-back", ActorID: "reviewer"})
	require.NoError(t, err)
	moved, err = e.Move(ctx, nil, MoveInput{CaseID: c.ID, Action: "send", ActorID: "beto"})
	require.NoError(t, err)
	assertInvariants(t, e, repo, c.ID)

	assert.Equal(t, "beto", moved.AssignedTo, "the newer actor, not the stored one")
}

func TestAssignRule_HistoryIsReadOnlyForNodesWithARule(t *testing.T) {
	a := &stubAssignees{answer: func(AssignInput) (string, error) { return "", nil }}
	repo := &countingEventsRepo{fakeRepository: newFakeRepository()}
	e := New(repo, Config{Assignees: a}, testLogger())
	require.NoError(t, e.Register(reviewDefinition()))
	c := openCase(t, e, "review")

	move(t, e, c.ID, uuid.Nil, "plain")
	assert.Zero(t, repo.lists, "a node without a rule reads no history")

	c2, err := e.Open(context.Background(), nil, OpenInput{Definition: "review", Domain: "contracts", ExternalID: uuid.NewString(), Unit: "hq", ActorID: "u1"})
	require.NoError(t, err)
	move(t, e, c2.ID, uuid.Nil, "send")
	assert.Equal(t, 1, repo.lists)
}

// countingEventsRepo counts history reads.
type countingEventsRepo struct {
	*fakeRepository
	lists int
}

func (c *countingEventsRepo) ListEvents(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Event, error) {
	c.lists++
	return c.fakeRepository.ListEvents(ctx, db, caseID)
}

func TestLastActorOf_SearchesWholeCase(t *testing.T) {
	inner, outer := uuid.New(), uuid.New()
	history := []Event{
		{Seq: 1, Kind: EventMoved, ScopeID: outer, FromState: "draft", ToState: "fork", Actor: Actor{Kind: ActorHuman, ID: "ana"}},
		{Seq: 2, Kind: EventMoved, ScopeID: inner, FromState: "review", ToState: "merge", Actor: Actor{Kind: ActorHuman, ID: "juan"}},
		{Seq: 3, Kind: EventAssigned, ScopeID: inner, FromState: "review", ToState: "review", Actor: Actor{Kind: ActorHuman, ID: "not-a-move"}},
		{Seq: 4, Kind: EventMoved, ScopeID: outer, FromState: "review", ToState: "merge", Actor: Actor{Kind: ActorHuman, ID: "pedro"}},
		{Seq: 5, Kind: EventMoved, ScopeID: outer, FromState: "other", ToState: "review", Actor: Actor{Kind: ActorHuman, ID: "entered-not-left"}},
	}
	assert.Equal(t, "pedro", LastActorOf(history, "review"), "the latest move leaving the node, in any scope")
	assert.Equal(t, "ana", LastActorOf(history, "draft"))
	assert.Empty(t, LastActorOf(history, "never"))
	assert.Empty(t, LastActorOf(nil, "review"))
}

func TestLastActorOf_IgnoresSystemActor(t *testing.T) {
	history := []Event{
		{Seq: 1, Kind: EventMoved, FromState: "review", ToState: "x", Actor: Actor{Kind: ActorHuman, ID: "ana"}},
		{Seq: 2, Kind: EventMoved, FromState: "review", ToState: "x", Actor: Actor{Kind: ActorSystem, ID: "scheduler"}},
	}
	assert.Equal(t, "ana", LastActorOf(history, "review"), "an expiry is not the person who held the node")
	assert.Empty(t, LastActorOf(history[1:], "review"))
}
