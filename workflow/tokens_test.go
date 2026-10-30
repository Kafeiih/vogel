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

	"github.com/kafeiih/vogel/pgxtx"
)

// tokenEngine returns an engine over a fresh fake repository with the
// gateway test definitions registered, and the guards they reference set from
// guards (a missing guard reads false).
func tokenEngine(t *testing.T, cfg Config, guards map[string]bool) (*Engine, *fakeRepository) {
	t.Helper()
	repo := newFakeRepository()
	e := New(repo, cfg, testLogger())
	for _, d := range []Definition{parallelDefinition(), inclusiveDefinition(), nestedDefinition()} {
		require.NoError(t, e.Register(d))
	}
	for _, name := range []string{"needs-legal", "needs-finance"} {
		pass := guards[name]
		require.NoError(t, e.RegisterGuard(name, func(context.Context, pgxtx.DBTX, Case) (bool, error) { return pass, nil }))
	}
	return e, repo
}

func openCase(t *testing.T, e *Engine, definition string) *Case {
	t.Helper()
	c, err := e.Open(context.Background(), nil, OpenInput{
		Definition: definition, Domain: "contracts", ExternalID: uuid.NewString(), Unit: "hq", ActorID: "u1",
	})
	require.NoError(t, err)
	return c
}

func move(t *testing.T, e *Engine, caseID, tokenID uuid.UUID, action string) *MoveResult {
	t.Helper()
	res, err := e.MoveDetailed(context.Background(), nil, MoveInput{CaseID: caseID, TokenID: tokenID, Action: action, ActorID: "u1"})
	require.NoError(t, err)
	return res
}

// activeTokens returns the case's active tokens by node.
func activeTokens(t *testing.T, e *Engine, caseID uuid.UUID) map[string]Token {
	t.Helper()
	tokens, err := e.Tokens(context.Background(), nil, caseID)
	require.NoError(t, err)
	out := make(map[string]Token)
	for _, tok := range tokens {
		if tok.Status == TokenActive {
			out[tok.Node] = tok
		}
	}
	return out
}

// splitToLegalAndFinance opens a parallelDefinition case and submits it, so
// "legal" and "finance" each hold one active token.
func splitToLegalAndFinance(t *testing.T, e *Engine, repo *fakeRepository) (*Case, map[string]Token) {
	t.Helper()
	c := openCase(t, e, "contract")
	move(t, e, c.ID, uuid.Nil, "submit")
	assertInvariants(t, e, repo, c.ID)
	return c, activeTokens(t, e, c.ID)
}

func TestMove_ParallelSplitCreatesOneTokenPerBranch(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)

	require.Len(t, active, 2)
	legal, finance := active["legal"], active["finance"]
	assert.NotEqual(t, c.ID, legal.ID, "a branch token is new")
	assert.Equal(t, legal.ScopeID, finance.ScopeID, "both branches share the split's scope")
	assert.NotEqual(t, c.ID, legal.ScopeID)
	assert.Equal(t, ByPositionInUnit("lawyer", "hq"), legal.Eligible)

	tokens, err := e.Tokens(context.Background(), nil, c.ID)
	require.NoError(t, err)
	root := tokens[0]
	assert.Equal(t, c.ID, root.ID)
	assert.Equal(t, TokenConsumed, root.Status, "the split consumed the arriving token")
	assert.Equal(t, "fork", root.Node)

	scopes, err := repo.ListScopes(context.Background(), nil, c.ID)
	require.NoError(t, err)
	require.Len(t, scopes, 2)
	assert.Equal(t, 2, scopes[1].Expected)
	assert.Equal(t, ScopeOpen, scopes[1].Status)
	assert.Equal(t, "fork", scopes[1].SplitNode)
	assert.Equal(t, "merge", scopes[1].JoinNode)
	assert.Equal(t, c.ID, *scopes[1].ParentID)
}

func TestMove_InclusiveSplitTakesEveryPassingRoute(t *testing.T) {
	for name, tc := range map[string]struct {
		guards map[string]bool
		want   []string
	}{
		"both pass":    {map[string]bool{"needs-legal": true, "needs-finance": true}, []string{"legal", "finance"}},
		"legal only":   {map[string]bool{"needs-legal": true}, []string{"legal"}},
		"finance only": {map[string]bool{"needs-finance": true}, []string{"finance"}},
	} {
		t.Run(name, func(t *testing.T) {
			e, repo := tokenEngine(t, Config{}, tc.guards)
			c := openCase(t, e, "contract-or")
			move(t, e, c.ID, uuid.Nil, "submit")
			assertInvariants(t, e, repo, c.ID)

			active := activeTokens(t, e, c.ID)
			nodes := make([]string, 0, len(active))
			for n := range active {
				nodes = append(nodes, n)
			}
			assert.ElementsMatch(t, tc.want, nodes)
			scopes, err := repo.ListScopes(context.Background(), nil, c.ID)
			require.NoError(t, err)
			assert.Equal(t, len(tc.want), scopes[1].Expected, "the scope expects only the tokens emitted")
		})
	}
}

func TestMove_InclusiveSplitFallsBackToDefault(t *testing.T) {
	// The default route goes straight to the join: the only branch arrives at
	// once, the join fires and the case closes in the same call.
	e, repo := tokenEngine(t, Config{}, nil)
	c := openCase(t, e, "contract-or")
	res := move(t, e, c.ID, uuid.Nil, "submit")
	assertInvariants(t, e, repo, c.ID)

	assert.Equal(t, []EventKind{EventMoved, EventSplit, EventMoved, EventWaiting, EventJoined, EventMoved, EventClosed}, eventKinds(res.Events))
	assert.Equal(t, "skip", res.Events[2].Action)
	assert.Equal(t, StatusClosed, res.Case.Status)
	assert.Equal(t, "approved", res.Case.State)
}

func TestMove_EmptyBranchArrivesAtJoinInSameCall(t *testing.T) {
	d := parallelDefinition()
	d.Transitions = append(d.Transitions, Transition{From: "fork", To: "merge", Action: "notify-only"})
	e, repo := tokenEngine(t, Config{}, nil)
	d.Version = 2
	require.NoError(t, e.Register(d))

	c := openCase(t, e, "contract")
	res := move(t, e, c.ID, uuid.Nil, "submit")
	assertInvariants(t, e, repo, c.ID)

	tokens, err := e.Tokens(context.Background(), nil, c.ID)
	require.NoError(t, err)
	waiting := 0
	for _, tok := range tokens {
		if tok.Status == TokenWaiting {
			waiting++
			assert.Equal(t, "merge", tok.Node)
		}
	}
	assert.Equal(t, 1, waiting, "the empty branch waits at the join")
	assert.Len(t, activeTokens(t, e, c.ID), 2)
	assert.Contains(t, eventKinds(res.Events), EventWaiting)
}

func TestMove_SplitAppendsSplitEventThenTokenHops(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c := openCase(t, e, "contract")
	res := move(t, e, c.ID, uuid.Nil, "submit")
	assertInvariants(t, e, repo, c.ID)

	require.Equal(t, []EventKind{EventMoved, EventSplit, EventMoved, EventMoved}, eventKinds(res.Events))
	human, split, toLegal, toFinance := res.Events[0], res.Events[1], res.Events[2], res.Events[3]
	assert.Equal(t, c.ID, human.TokenID)
	assert.Equal(t, "fork", human.ToState)

	assert.Equal(t, c.ID, split.TokenID, "the split event names the consumed token")
	assert.Equal(t, c.ID, split.ScopeID)
	var detail SplitDetail
	require.NoError(t, json.Unmarshal(split.Detail, &detail))
	assert.Equal(t, "fork", detail.SplitNode)
	assert.Equal(t, []uuid.UUID{toLegal.TokenID, toFinance.TokenID}, detail.Tokens)
	assert.Equal(t, detail.Scope, toLegal.ScopeID)
	assert.Equal(t, detail.Scope, toFinance.ScopeID)

	assert.Equal(t, "fork", toLegal.FromState)
	assert.Equal(t, "legal", toLegal.ToState)
	assert.Equal(t, "to-legal", toLegal.Action)
	assert.Equal(t, "finance", toFinance.ToState)
	for i, ev := range res.Events {
		assert.Equal(t, c.LastSeq+int64(i)+1, ev.Seq)
	}
}

func TestMove_JoinWaitsUntilEveryBranchArrives(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)

	res := move(t, e, c.ID, active["legal"].ID, "clear")
	assertInvariants(t, e, repo, c.ID)
	assert.Equal(t, []EventKind{EventMoved, EventWaiting}, eventKinds(res.Events))
	assert.Empty(t, res.Joined)
	assert.Equal(t, StatusOpen, res.Case.Status)
	assert.Equal(t, "finance", res.Case.State, "with one active token left the projection shows it")

	tokens, err := e.Tokens(context.Background(), nil, c.ID)
	require.NoError(t, err)
	for _, tok := range tokens {
		if tok.ID == active["legal"].ID {
			assert.Equal(t, TokenWaiting, tok.Status)
			assert.Equal(t, "merge", tok.Node)
		}
	}
}

func TestMove_JoinFiresOnceAndContinuesInParentScope(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	move(t, e, c.ID, active["legal"].ID, "clear")

	res := move(t, e, c.ID, active["finance"].ID, "clear")
	assertInvariants(t, e, repo, c.ID)
	assert.Equal(t, []EventKind{EventMoved, EventWaiting, EventJoined, EventMoved}, eventKinds(res.Events))

	joined := res.Events[2]
	var detail JoinDetail
	require.NoError(t, json.Unmarshal(joined.Detail, &detail))
	assert.Equal(t, active["legal"].ScopeID, detail.Scope)
	assert.Equal(t, joined.ScopeID, detail.Scope)
	assert.Equal(t, "merge", detail.JoinNode)
	assert.ElementsMatch(t, []uuid.UUID{active["legal"].ID, active["finance"].ID}, detail.Tokens)

	next := activeTokens(t, e, c.ID)["signoff"]
	assert.Equal(t, c.ID, next.ScopeID, "the joined token lives in the parent (root) scope")
	assert.Equal(t, joined.TokenID, next.ID, "the joined event names the token it creates")
	assert.Equal(t, "signoff", res.Case.State)

	// Single-token semantics again: no TokenID needed.
	res = move(t, e, c.ID, uuid.Nil, "approve")
	assert.Equal(t, StatusClosed, res.Case.Status)
	assertInvariants(t, e, repo, c.ID)
}

func TestMove_NestedSplitsJoinInnerThenOuter(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c := openCase(t, e, "contract-nested")
	move(t, e, c.ID, uuid.Nil, "submit")
	assertInvariants(t, e, repo, c.ID)

	active := activeTokens(t, e, c.ID)
	require.Len(t, active, 3)
	assert.Equal(t, active["contracts"].ScopeID, active["compliance"].ScopeID)
	assert.NotEqual(t, active["contracts"].ScopeID, active["finance"].ScopeID)

	move(t, e, c.ID, active["finance"].ID, "clear")
	assertInvariants(t, e, repo, c.ID)
	move(t, e, c.ID, active["contracts"].ID, "clear")
	assertInvariants(t, e, repo, c.ID)

	res := move(t, e, c.ID, active["compliance"].ID, "clear")
	assertInvariants(t, e, repo, c.ID)
	// The inner join fires, its token goes straight to the outer join and
	// completes it, and the outer token closes the case.
	assert.Equal(t, []EventKind{
		EventMoved, EventWaiting, EventJoined, EventMoved, EventWaiting, EventJoined, EventMoved, EventClosed,
	}, eventKinds(res.Events))
	require.Len(t, res.Joined, 2)
	assert.Equal(t, "merge2", res.Joined[0].Scope.JoinNode)
	assert.Equal(t, "merge", res.Joined[1].Scope.JoinNode)
	assert.Equal(t, StatusClosed, res.Case.Status)
}

func TestMove_InclusiveJoinWaitsOnlyForEmittedTokens(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, map[string]bool{"needs-legal": true})
	c := openCase(t, e, "contract-or")
	move(t, e, c.ID, uuid.Nil, "submit")

	res := move(t, e, c.ID, uuid.Nil, "clear")
	assertInvariants(t, e, repo, c.ID)
	assert.Len(t, res.Joined, 1, "one emitted token: its arrival completes the join")
	assert.Equal(t, StatusClosed, res.Case.Status)
}

// TestEngine_ConcurrentLastBranches_OneConflictThenSingleJoin completes the
// last two branches of one case from two stale reads: the slower write fails
// with ErrConflict, and its retry sees the other branch waiting and joins
// exactly once.
func TestEngine_ConcurrentLastBranches_OneConflictThenSingleJoin(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)

	racing := &racingRepository{fakeRepository: repo}
	slow := New(racing, Config{}, testLogger(), WithDefinitions(parallelDefinition()))
	racing.beforeUpdate = func() {
		// The other branch commits between the slow call's read and write.
		move(t, e, c.ID, active["legal"].ID, "clear")
	}

	_, err := slow.Move(context.Background(), nil, MoveInput{CaseID: c.ID, TokenID: active["finance"].ID, Action: "clear", ActorID: "u2"})
	require.ErrorIs(t, err, ErrConflict)
	assertInvariants(t, e, repo, c.ID)
	assert.Empty(t, joinedEvents(t, e, c.ID), "the conflicting call persisted nothing")

	res, err := slow.MoveDetailed(context.Background(), nil, MoveInput{CaseID: c.ID, TokenID: active["finance"].ID, Action: "clear", ActorID: "u2"})
	require.NoError(t, err)
	assert.Len(t, res.Joined, 1)
	assertInvariants(t, e, repo, c.ID)
	assert.Len(t, joinedEvents(t, e, c.ID), 1)
}

func joinedEvents(t *testing.T, e *Engine, caseID uuid.UUID) []Event {
	t.Helper()
	events, err := e.History(context.Background(), nil, caseID)
	require.NoError(t, err)
	var out []Event
	for _, ev := range events {
		if ev.Kind == EventJoined {
			out = append(out, ev)
		}
	}
	return out
}

func TestMove_SingleTokenCaseNeedsNoTokenID(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c := openCase(t, e, "contract")
	tokens, err := e.Tokens(context.Background(), nil, c.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.Equal(t, c.ID, tokens[0].ID, "the root token shares the case ID")
	assert.Equal(t, c.ID, tokens[0].ScopeID)

	res := move(t, e, c.ID, uuid.Nil, "submit")
	assert.Equal(t, c.ID, res.Events[0].TokenID)
	assertInvariants(t, e, repo, c.ID)

	// Naming the root token explicitly works too.
	e2, repo2 := tokenEngine(t, Config{}, nil)
	c2 := openCase(t, e2, "contract")
	move(t, e2, c2.ID, c2.ID, "submit")
	assertInvariants(t, e2, repo2, c2.ID)
}

func TestMove_SeveralActiveTokensRequireTokenID(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, _ := splitToLegalAndFinance(t, e, repo)

	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "clear", ActorID: "u1"})
	require.ErrorIs(t, err, ErrTokenRequired)
	_, err = e.Available(context.Background(), nil, c.ID)
	require.ErrorIs(t, err, ErrTokenRequired)
	_, err = e.EligibilityFor(context.Background(), nil, c.ID)
	require.ErrorIs(t, err, ErrTokenRequired)
	_, err = e.Claim(context.Background(), nil, c.ID, "u1")
	require.ErrorIs(t, err, ErrTokenRequired)
	assertInvariants(t, e, repo, c.ID)
}

func TestMove_UnknownTokenIsNotFound(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)

	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, TokenID: uuid.New(), Action: "clear", ActorID: "u1"})
	require.ErrorIs(t, err, ErrTokenNotFound)
	// The consumed root token is no longer active.
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, TokenID: c.ID, Action: "clear", ActorID: "u1"})
	require.ErrorIs(t, err, ErrTokenNotFound)
	// A waiting token is not active either.
	move(t, e, c.ID, active["legal"].ID, "clear")
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, TokenID: active["legal"].ID, Action: "clear", ActorID: "u1"})
	require.ErrorIs(t, err, ErrTokenNotFound)
	_, err = e.AvailableFor(context.Background(), nil, c.ID, active["legal"].ID)
	require.ErrorIs(t, err, ErrTokenNotFound)
	assertInvariants(t, e, repo, c.ID)
}

func TestAvailableFor_AndEligibilityForToken_ArePerToken(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)

	trs, err := e.AvailableFor(context.Background(), nil, c.ID, active["legal"].ID)
	require.NoError(t, err)
	require.Len(t, trs, 1)
	assert.Equal(t, "legal", trs[0].From)

	el, err := e.EligibilityForToken(context.Background(), nil, c.ID, active["finance"].ID)
	require.NoError(t, err)
	assert.Equal(t, ByPositionInUnit("analyst", "hq"), el)
}

func TestClaim_IsPerToken(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)

	_, err := e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, TokenID: active["legal"].ID, Actor: Actor{ID: "lawyer-1"}})
	require.NoError(t, err)
	_, err = e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, TokenID: active["finance"].ID, Actor: Actor{ID: "analyst-1"}})
	require.NoError(t, err, "another branch can be claimed by someone else")
	_, err = e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, TokenID: active["legal"].ID, Actor: Actor{ID: "analyst-1"}})
	require.ErrorIs(t, err, ErrAlreadyAssigned)
	assertInvariants(t, e, repo, c.ID)

	now := activeTokens(t, e, c.ID)
	assert.Equal(t, "lawyer-1", now["legal"].AssignedTo)
	assert.Equal(t, "analyst-1", now["finance"].AssignedTo)

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	last := events[len(events)-1]
	assert.Equal(t, EventAssigned, last.Kind)
	assert.Equal(t, active["finance"].ID, last.TokenID)
	assert.Equal(t, "finance", last.FromState)
}

func TestRelease_IsPerToken(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	_, err := e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, TokenID: active["legal"].ID, Actor: Actor{ID: "lawyer-1"}})
	require.NoError(t, err)

	_, err = e.ReleaseWith(context.Background(), nil, ReleaseInput{CaseID: c.ID, TokenID: active["finance"].ID, Actor: Actor{ID: "lawyer-1"}})
	require.ErrorIs(t, err, ErrNotAssigned, "the finance branch was never claimed")
	_, err = e.ReleaseWith(context.Background(), nil, ReleaseInput{CaseID: c.ID, TokenID: active["legal"].ID, Actor: Actor{ID: "lawyer-1"}, Reason: "vacation"})
	require.NoError(t, err)
	assert.Equal(t, "", activeTokens(t, e, c.ID)["legal"].AssignedTo)
	assertInvariants(t, e, repo, c.ID)
}

func TestCaseProjection_StateEmptyWhileParallel(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	_, err := e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, TokenID: active["legal"].ID, Actor: Actor{ID: "lawyer-1"}})
	require.NoError(t, err)

	got, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "", got.State, "several active tokens: no single state")
	assert.Equal(t, "", got.AssignedTo, "several active tokens: no single assignee")

	move(t, e, c.ID, active["finance"].ID, "clear")
	got, err = repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "legal", got.State)
	assert.Equal(t, "lawyer-1", got.AssignedTo)
	assertInvariants(t, e, repo, c.ID)
}

func TestInbox_ReturnsOneWorkItemPerActiveToken(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	other := openCase(t, e, "contract")

	items, err := e.Inbox(context.Background(), nil, InboxFilter{Domain: "contracts"})
	require.NoError(t, err)
	require.Len(t, items, 3)
	byToken := make(map[uuid.UUID]WorkItem)
	for _, it := range items {
		byToken[it.Token.ID] = it
	}
	assert.Equal(t, c.ID, byToken[active["legal"].ID].Case.ID)
	assert.Equal(t, c.ID, byToken[active["finance"].ID].Case.ID)
	assert.Equal(t, other.ID, byToken[other.ID].Case.ID)

	items, err = e.Inbox(context.Background(), nil, InboxFilter{States: []string{"legal"}})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, active["legal"].ID, items[0].Token.ID)

	items, err = e.Inbox(context.Background(), nil, InboxFilter{Eligibility: []Eligibility{ByPositionInUnit("analyst", "hq")}})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, active["finance"].ID, items[0].Token.ID)

	_, err = e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, TokenID: active["legal"].ID, Actor: Actor{ID: "lawyer-1"}})
	require.NoError(t, err)
	items, err = e.Inbox(context.Background(), nil, InboxFilter{AssignedTo: "lawyer-1"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, active["legal"].ID, items[0].Token.ID)
	items, err = e.Inbox(context.Background(), nil, InboxFilter{Domain: "contracts", Unassigned: true})
	require.NoError(t, err)
	assert.Len(t, items, 2)
}

func TestInbox_PagesTokensNotCases(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	splitToLegalAndFinance(t, e, repo)
	splitToLegalAndFinance(t, e, repo)

	var seen []uuid.UUID
	for offset := 0; offset < 4; offset += 2 {
		page, err := e.Inbox(context.Background(), nil, InboxFilter{Limit: 2, Offset: offset})
		require.NoError(t, err)
		require.Len(t, page, 2, "two cases hold four work items: two full pages")
		for _, it := range page {
			seen = append(seen, it.Token.ID)
		}
	}
	assert.Len(t, seen, 4)
	page, err := e.Inbox(context.Background(), nil, InboxFilter{Limit: 2, Offset: 4})
	require.NoError(t, err)
	assert.Empty(t, page)
}

// returnInBranchDefinition is parallelDefinition with a two-step "legal"
// branch whose second step can return to the first.
func returnInBranchDefinition() Definition {
	d := parallelDefinition()
	d.Version = 2
	d.Nodes = append(d.Nodes, Node{ID: "legal-review"})
	d.Transitions[3] = Transition{From: "legal", To: "legal-review", Action: "clear"}
	d.Transitions = append(d.Transitions,
		Transition{From: "legal-review", To: "merge", Action: "clear"},
		Transition{From: "legal-review", To: "legal", Action: "back", Return: true},
		Transition{From: "signoff", To: "legal-review", Action: "back-to-review", Return: true},
	)
	return d
}

func TestReturn_InsideBranchUsesScopeHistoryOnly(t *testing.T) {
	d := returnInBranchDefinition()
	// signoff -> legal-review crosses the block: Validate rejects it.
	require.ErrorIs(t, d.Validate(), ErrInvalidDefinition)
	d.Transitions = d.Transitions[:len(d.Transitions)-1]

	e, repo := tokenEngine(t, Config{}, nil)
	require.NoError(t, e.Register(d))
	c, active := splitToLegalAndFinance(t, e, repo)
	legal := active["legal"].ID

	move(t, e, c.ID, legal, "clear")
	trs, err := e.AvailableFor(context.Background(), nil, c.ID, legal)
	require.NoError(t, err)
	assert.Len(t, trs, 2, "legal was occupied in this scope: the return is offered")
	move(t, e, c.ID, legal, "back")
	assertInvariants(t, e, repo, c.ID)

	// Only the scope's own events count: occupiedStates for the finance
	// branch's scope sees legal (same scope), the root scope does not.
	occupiedRoot, err := e.occupiedStates(context.Background(), nil, c.ID, c.ID)
	require.NoError(t, err)
	assert.False(t, occupiedRoot["legal"], "the branch's history is not the root scope's")
	assert.True(t, occupiedRoot["draft"])
	occupiedBranch, err := e.occupiedStates(context.Background(), nil, c.ID, active["legal"].ScopeID)
	require.NoError(t, err)
	assert.True(t, occupiedBranch["legal-review"])
	assert.False(t, occupiedBranch["draft"], "the root scope's history is not the branch's")
}

func TestCancel_RequiresReason(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)

	_, err := e.Cancel(context.Background(), nil, CancelInput{CaseID: c.ID, Actor: Actor{ID: "u1"}, Reason: "  "})
	require.ErrorIs(t, err, ErrReasonRequired)
	_, err = e.CancelToken(context.Background(), nil, CancelTokenInput{CaseID: c.ID, TokenID: active["legal"].ID, Actor: Actor{ID: "u1"}})
	require.ErrorIs(t, err, ErrReasonRequired)
	assert.Len(t, activeTokens(t, e, c.ID), 2, "nothing was cancelled")
	assertInvariants(t, e, repo, c.ID)
}

func TestCancel_CancelsEveryTokenWithOneEventEach(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	move(t, e, c.ID, active["legal"].ID, "clear") // legal waits at the join

	got, err := e.Cancel(context.Background(), nil, CancelInput{CaseID: c.ID, Actor: Actor{ID: "u1"}, Reason: "withdrawn"})
	require.NoError(t, err)
	assert.Equal(t, StatusCancelled, got.Status)
	require.NotNil(t, got.ClosedAt)
	assertInvariants(t, e, repo, c.ID)

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	tail := events[len(events)-3:]
	assert.Equal(t, []EventKind{EventTokenCancelled, EventTokenCancelled, EventCancelled}, eventKinds(tail))
	assert.ElementsMatch(t, []uuid.UUID{active["legal"].ID, active["finance"].ID}, []uuid.UUID{tail[0].TokenID, tail[1].TokenID})
	var detail CancelDetail
	require.NoError(t, json.Unmarshal(tail[2].Detail, &detail))
	assert.Equal(t, "withdrawn", detail.Reason)

	scopes, err := repo.ListScopes(context.Background(), nil, c.ID)
	require.NoError(t, err)
	for _, s := range scopes {
		assert.Equal(t, ScopeCancelled, s.Status)
	}
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, TokenID: active["finance"].ID, Action: "clear", ActorID: "u1"})
	require.ErrorIs(t, err, ErrCaseClosed)
	_, err = e.Cancel(context.Background(), nil, CancelInput{CaseID: c.ID, Actor: Actor{ID: "u1"}, Reason: "again"})
	require.ErrorIs(t, err, ErrCaseClosed)
	assert.True(t, StatusCancelled.Valid())
}

func TestCancelToken_CompletesJoinWhenOthersWait(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	move(t, e, c.ID, active["legal"].ID, "clear")

	res, err := e.CancelToken(context.Background(), nil, CancelTokenInput{
		CaseID: c.ID, TokenID: active["finance"].ID, Actor: Actor{ID: "u1"}, Reason: "not needed",
	})
	require.NoError(t, err)
	assertInvariants(t, e, repo, c.ID)
	assert.Equal(t, []EventKind{EventTokenCancelled, EventJoined, EventMoved}, eventKinds(res.Events))
	require.Len(t, res.Joined, 1)
	assert.Len(t, res.Joined[0].Tokens, 1, "the join consumes only the waiting token")
	assert.Equal(t, "signoff", res.Case.State)
}

func TestCancelToken_RejectsLastBranch(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)

	_, err := e.CancelToken(context.Background(), nil, CancelTokenInput{CaseID: c.ID, TokenID: active["legal"].ID, Actor: Actor{ID: "u1"}, Reason: "x"})
	require.NoError(t, err, "finance is still live")
	_, err = e.CancelToken(context.Background(), nil, CancelTokenInput{CaseID: c.ID, TokenID: active["finance"].ID, Actor: Actor{ID: "u1"}, Reason: "x"})
	require.ErrorIs(t, err, ErrLastBranch)
	assertInvariants(t, e, repo, c.ID)

	single := openCase(t, e, "contract")
	_, err = e.CancelToken(context.Background(), nil, CancelTokenInput{CaseID: single.ID, TokenID: single.ID, Actor: Actor{ID: "u1"}, Reason: "x"})
	require.ErrorIs(t, err, ErrLastBranch, "the root token is the root scope's only branch")
}

func TestDeadline_IsSetPerTokenOnEntry(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	d := parallelDefinition()
	d.Version = 3
	d.Nodes[2].Deadline = 2 * time.Hour // legal
	d.Nodes[3].Deadline = time.Hour     // finance
	e, repo := tokenEngine(t, Config{Now: func() time.Time { return now }}, nil)
	require.NoError(t, e.Register(d))

	c, active := splitToLegalAndFinance(t, e, repo)
	require.NotNil(t, active["legal"].DeadlineAt)
	assert.Equal(t, now.Add(2*time.Hour), *active["legal"].DeadlineAt)
	assert.Equal(t, now.Add(time.Hour), *active["finance"].DeadlineAt)

	got, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	require.NotNil(t, got.DeadlineAt)
	assert.Equal(t, now.Add(time.Hour), *got.DeadlineAt, "the case shows the earliest active deadline")
}

func TestInbox_OverdueIsPerToken(t *testing.T) {
	past := time.Now().Add(-3 * time.Hour)
	d := parallelDefinition()
	d.Version = 3
	d.Nodes[3].Deadline = time.Hour // finance only
	e, repo := tokenEngine(t, Config{Now: func() time.Time { return past }}, nil)
	require.NoError(t, e.Register(d))
	_, active := splitToLegalAndFinance(t, e, repo)

	items, err := e.Inbox(context.Background(), nil, InboxFilter{Overdue: true})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, active["finance"].ID, items[0].Token.ID)
}

func TestEvents_CarryTokenAndScope(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	move(t, e, c.ID, active["legal"].ID, "clear")

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	for _, ev := range events {
		assert.NotEqual(t, uuid.Nil, ev.TokenID, "seq %d", ev.Seq)
		assert.NotEqual(t, uuid.Nil, ev.ScopeID, "seq %d", ev.Seq)
		assert.Equal(t, CurrentEventSchema, ev.SchemaVersion)
	}
	last := events[len(events)-1]
	assert.Equal(t, EventWaiting, last.Kind)
	assert.Equal(t, active["legal"].ID, last.TokenID)
	assert.Equal(t, active["legal"].ScopeID, last.ScopeID)
}

func TestEvents_LegacyRowWithoutTokenReadsAsRoot(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())
	caseID := uuid.New()
	require.NoError(t, repo.Create(context.Background(), nil, &Case{ID: caseID, Domain: "d", ExternalID: "1"}))
	require.NoError(t, repo.AppendEvent(context.Background(), nil, &Event{ID: uuid.New(), CaseID: caseID, Seq: 1, Kind: EventOpened, SchemaVersion: 2}))

	events, err := e.History(context.Background(), nil, caseID)
	require.NoError(t, err)
	assert.Equal(t, caseID, events[0].TokenID)
	assert.Equal(t, caseID, events[0].ScopeID)
}

func TestEngine_SynthesizesRootTokenForCaseWithoutTokens(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c := openCase(t, e, "contract")
	_, err := e.ClaimWith(context.Background(), nil, ClaimInput{CaseID: c.ID, Actor: Actor{ID: "buyer-1"}})
	require.NoError(t, err)
	// An old binary wrote the case after migration 008: no token rows.
	repo.mu.Lock()
	for id, tok := range repo.tokens {
		if tok.CaseID == c.ID {
			delete(repo.tokens, id)
		}
	}
	for id, s := range repo.scopes {
		if s.CaseID == c.ID {
			delete(repo.scopes, id)
		}
	}
	repo.mu.Unlock()

	tokens, err := e.Tokens(context.Background(), nil, c.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.Equal(t, c.ID, tokens[0].ID)
	assert.Equal(t, "draft", tokens[0].Node)
	assert.Equal(t, "buyer-1", tokens[0].AssignedTo)
	assert.Equal(t, int64(1), tokens[0].EnteredSeq)
	assert.Empty(t, repo.tokens, "a read does not persist the synthesized token")

	move(t, e, c.ID, uuid.Nil, "submit")
	assert.Len(t, activeTokens(t, e, c.ID), 2)
	assertInvariants(t, e, repo, c.ID)
}

func TestJoinCheck_RejectionAbortsMoveAndPersistsNothing(t *testing.T) {
	conflict := errors.New("both branches set the same field")
	var seen []JoinRecord
	e, repo := tokenEngine(t, Config{JoinCheck: func(_ context.Context, _ pgxtx.DBTX, j JoinRecord) error {
		seen = append(seen, j)
		return conflict
	}}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	move(t, e, c.ID, active["legal"].ID, "clear")
	before, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	tokensBefore, err := e.Tokens(context.Background(), nil, c.ID)
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, TokenID: active["finance"].ID, Action: "clear", ActorID: "u1"})
	require.ErrorIs(t, err, ErrJoinRejected)
	require.ErrorIs(t, err, conflict)
	require.Len(t, seen, 1)
	assert.Equal(t, "merge", seen[0].Scope.JoinNode)
	assert.Len(t, seen[0].Tokens, 2)

	after, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	tokensAfter, err := e.Tokens(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, tokensBefore, tokensAfter)
	assert.Empty(t, joinedEvents(t, e, c.ID))
}

func TestMoveResult_ReportsJoinedScopes(t *testing.T) {
	e, repo := tokenEngine(t, Config{JoinCheck: func(context.Context, pgxtx.DBTX, JoinRecord) error { return nil }}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	move(t, e, c.ID, active["legal"].ID, "clear")
	res := move(t, e, c.ID, active["finance"].ID, "clear")

	require.Len(t, res.Joined, 1)
	j := res.Joined[0]
	assert.Equal(t, active["legal"].ScopeID, j.Scope.ID)
	assert.Equal(t, ScopeJoined, j.Scope.Status)
	ids := []uuid.UUID{j.Tokens[0].ID, j.Tokens[1].ID}
	assert.ElementsMatch(t, []uuid.UUID{active["legal"].ID, active["finance"].ID}, ids)
	for _, tok := range j.Tokens {
		assert.Equal(t, TokenConsumed, tok.Status)
	}
}

// assertInvariants checks S13's I1-I8 for caseID against what the repository
// stores.
func assertInvariants(t *testing.T, e *Engine, repo *fakeRepository, caseID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	c, err := repo.GetByID(ctx, nil, caseID)
	require.NoError(t, err)
	d, err := e.resolveDefinition(c.Definition, c.Version)
	require.NoError(t, err)
	tokens, err := repo.ListTokens(ctx, nil, caseID)
	require.NoError(t, err)
	scopes, err := repo.ListScopes(ctx, nil, caseID)
	require.NoError(t, err)
	scopeByID := make(map[uuid.UUID]Scope, len(scopes))
	for _, s := range scopes {
		scopeByID[s.ID] = s
	}

	var active, waiting []Token
	// branches counts one token per branch of each scope: a token a nested
	// split consumed is continued by the token that split's join creates in
	// the same scope, so it is not counted; until that join fires, the
	// nested scope stands for the branch.
	branches := make(map[uuid.UUID]int)
	for _, s := range scopes {
		if s.ParentID != nil && s.Status != ScopeJoined {
			branches[*s.ParentID]++
		}
	}
	for _, tok := range tokens {
		if n, _ := d.Node(tok.Node); !(tok.Status == TokenConsumed && n.Kind.IsSplit()) {
			branches[tok.ScopeID]++
		}
		switch tok.Status {
		case TokenActive:
			active = append(active, tok)
		case TokenWaiting:
			waiting = append(waiting, tok)
		}
	}

	// I1, I2
	assert.Equal(t, c.Status == StatusOpen, len(active) > 0, "I1: open iff an active token")
	assert.Equal(t, c.Status != StatusOpen, len(active) == 0 && len(waiting) == 0, "I2: ended iff no live token")
	// I3
	for _, tok := range waiting {
		s := scopeByID[tok.ScopeID]
		assert.Equal(t, ScopeOpen, s.Status, "I3: waiting token %s in an open scope", tok.ID)
		assert.Equal(t, s.JoinNode, tok.Node, "I3: waiting token %s at its join", tok.ID)
	}
	// I4
	for _, s := range scopes {
		assert.Equal(t, s.Expected, branches[s.ID], "I4: scope %s expects one token per branch", s.ID)
	}
	// I5
	for _, tok := range active {
		n, ok := d.Node(tok.Node)
		require.True(t, ok)
		assert.False(t, n.Kind.IsAutomatic() || n.Terminal, "I5: token %s rests on %q", tok.ID, tok.Node)
	}
	// I6
	if c.Status == StatusOpen {
		wantState, wantAssigned := "", ""
		if len(active) == 1 {
			wantState, wantAssigned = active[0].Node, active[0].AssignedTo
		}
		assert.Equal(t, wantState, c.State, "I6: State projection")
		assert.Equal(t, wantAssigned, c.AssignedTo, "I6: AssignedTo projection")
		var earliest *time.Time
		for _, tok := range active {
			if tok.DeadlineAt != nil && (earliest == nil || tok.DeadlineAt.Before(*earliest)) {
				earliest = tok.DeadlineAt
			}
		}
		if earliest == nil {
			assert.Nil(t, c.DeadlineAt, "I6: DeadlineAt projection")
		} else if assert.NotNil(t, c.DeadlineAt, "I6: DeadlineAt projection") {
			assert.True(t, earliest.Equal(*c.DeadlineAt), "I6: DeadlineAt projection")
		}
	}
	// I7
	events, err := e.History(ctx, nil, caseID)
	require.NoError(t, err)
	joins := make(map[uuid.UUID]int)
	for _, ev := range events {
		if ev.Kind == EventJoined {
			joins[ev.ScopeID]++
			assert.Equal(t, 1, joins[ev.ScopeID], "I7: scope %s joined once", ev.ScopeID)
		}
	}
	// I8
	wantTokens, wantScopes := replay(t, d, c, events)
	assert.Equal(t, comparableTokens(wantTokens), comparableTokens(tokens), "I8: replayed tokens")
	assert.Equal(t, comparableScopes(wantScopes), comparableScopes(scopes), "I8: replayed scopes")
}

// replay rebuilds a case's tokens and scopes from its events alone (TR-22).
func replay(t *testing.T, d Definition, c *Case, events []Event) ([]Token, []Scope) {
	t.Helper()
	tokens := make(map[uuid.UUID]*Token)
	scopes := make(map[uuid.UUID]*Scope)
	var tokenOrder, scopeOrder []uuid.UUID
	newToken := func(id, scope uuid.UUID, node string) *Token {
		tok := &Token{ID: id, CaseID: c.ID, ScopeID: scope, Node: node, Status: TokenActive}
		tokens[id] = tok
		tokenOrder = append(tokenOrder, id)
		return tok
	}
	end := func(tok *Token, status TokenStatus, ev Event) {
		at := ev.OccurredAt
		tok.Status, tok.EndedSeq, tok.EndedAt = status, ev.Seq, &at
		tok.AssignedTo, tok.DeadlineAt = "", nil
	}
	enter := func(tok *Token, node string, ev Event) {
		tok.Node, tok.Status = node, TokenActive
		tok.EnteredSeq, tok.EnteredAt = ev.Seq, ev.OccurredAt
		tok.AssignedTo, tok.DeadlineAt, tok.Eligible = "", nil, Eligibility{}
		n, _ := d.Node(node)
		if !n.Kind.IsAutomatic() && !n.Terminal {
			tok.Eligible = resolvedEligibility(n, c.Unit)
			if n.Deadline > 0 {
				dl := ev.OccurredAt.Add(n.Deadline)
				tok.DeadlineAt = &dl
			}
		}
	}
	for _, ev := range events {
		switch ev.Kind {
		case EventOpened:
			scopes[c.ID] = &Scope{ID: c.ID, CaseID: c.ID, Expected: 1, Status: ScopeOpen, OpenedSeq: ev.Seq}
			scopeOrder = append(scopeOrder, c.ID)
			enter(newToken(c.ID, c.ID, ev.ToState), ev.ToState, ev)
		case EventMoved:
			tok, ok := tokens[ev.TokenID]
			if !ok {
				tok = newToken(ev.TokenID, ev.ScopeID, ev.FromState)
			}
			enter(tok, ev.ToState, ev)
		case EventSplit:
			var detail SplitDetail
			require.NoError(t, json.Unmarshal(ev.Detail, &detail))
			end(tokens[ev.TokenID], TokenConsumed, ev)
			parent := ev.ScopeID
			split, _ := d.Node(detail.SplitNode)
			scopes[detail.Scope] = &Scope{
				ID: detail.Scope, CaseID: c.ID, ParentID: &parent, SplitNode: detail.SplitNode,
				JoinNode: split.Join, Expected: len(detail.Tokens), Status: ScopeOpen, OpenedSeq: ev.Seq,
			}
			scopeOrder = append(scopeOrder, detail.Scope)
		case EventWaiting:
			tokens[ev.TokenID].Status = TokenWaiting
		case EventJoined:
			var detail JoinDetail
			require.NoError(t, json.Unmarshal(ev.Detail, &detail))
			for _, id := range detail.Tokens {
				end(tokens[id], TokenConsumed, ev)
			}
			s := scopes[detail.Scope]
			s.Status, s.ClosedSeq = ScopeJoined, ev.Seq
			newToken(ev.TokenID, *s.ParentID, detail.JoinNode)
		case EventClosed:
			end(tokens[ev.TokenID], TokenConsumed, ev)
			scopes[c.ID].Status, scopes[c.ID].ClosedSeq = ScopeClosed, ev.Seq
		case EventTokenCancelled:
			end(tokens[ev.TokenID], TokenCancelled, ev)
		case EventCancelled:
			for _, s := range scopes {
				if s.Status == ScopeOpen {
					s.Status, s.ClosedSeq = ScopeCancelled, ev.Seq
				}
			}
		case EventAssigned, EventUnassigned:
			var detail AssignmentDetail
			require.NoError(t, json.Unmarshal(ev.Detail, &detail))
			tokens[ev.TokenID].AssignedTo = detail.To
		}
	}
	outT := make([]Token, 0, len(tokenOrder))
	for _, id := range tokenOrder {
		outT = append(outT, *tokens[id])
	}
	outS := make([]Scope, 0, len(scopeOrder))
	for _, id := range scopeOrder {
		outS = append(outS, *scopes[id])
	}
	return outT, outS
}

// comparableTokens and comparableScopes render rows as strings keyed by ID,
// so two sets compare regardless of order and of time.Time internals.
func comparableTokens(tokens []Token) map[uuid.UUID]string {
	out := make(map[uuid.UUID]string, len(tokens))
	ts := func(p *time.Time) string {
		if p == nil {
			return "nil"
		}
		return p.UTC().Format(time.RFC3339Nano)
	}
	for _, tok := range tokens {
		out[tok.ID] = fmt.Sprintf("scope=%s node=%s status=%s eligible=%v assigned=%q deadline=%s entered=%d@%s ended=%d@%s",
			tok.ScopeID, tok.Node, tok.Status, tok.Eligible, tok.AssignedTo, ts(tok.DeadlineAt),
			tok.EnteredSeq, tok.EnteredAt.UTC().Format(time.RFC3339Nano), tok.EndedSeq, ts(tok.EndedAt))
	}
	return out
}

func comparableScopes(scopes []Scope) map[uuid.UUID]string {
	out := make(map[uuid.UUID]string, len(scopes))
	for _, s := range scopes {
		parent := "root"
		if s.ParentID != nil {
			parent = s.ParentID.String()
		}
		out[s.ID] = fmt.Sprintf("parent=%s split=%s join=%s expected=%d status=%s opened=%d closed=%d",
			parent, s.SplitNode, s.JoinNode, s.Expected, s.Status, s.OpenedSeq, s.ClosedSeq)
	}
	return out
}

func TestRefreshEligibility_WritesMissingEligibility(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c := openCase(t, e, "contract")
	// Migration 008 materializes tokens without position/unit.
	repo.mu.Lock()
	tok := repo.tokens[c.ID]
	tok.Eligible = Eligibility{}
	repo.tokens[c.ID] = tok
	repo.mu.Unlock()
	filter := InboxFilter{Eligibility: []Eligibility{ByPositionInUnit("buyer", "hq")}}

	items, err := e.Inbox(context.Background(), nil, filter)
	require.NoError(t, err)
	assert.Empty(t, items, "a token stored without eligibility does not match")

	wrote, err := e.RefreshEligibility(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.True(t, wrote)
	items, err = e.Inbox(context.Background(), nil, filter)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, c.ID, items[0].Token.ID)

	wrote, err = e.RefreshEligibility(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.False(t, wrote, "nothing left to write")
	history, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Len(t, history, 1, "no event is appended")
	assertInvariants(t, e, repo, c.ID)
}

// TestInbox_KeepsCaseOrderThenTokenOrder pins the inbox order: oldest case
// first, as before tokens existed, then the case's tokens by when they
// entered their node.
func TestInbox_KeepsCaseOrderThenTokenOrder(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	e, repo := tokenEngine(t, Config{Now: clock}, nil)

	older := openCase(t, e, "contract") // opened 09:00
	now = now.Add(time.Hour)
	newer := openCase(t, e, "contract") // opened 10:00, never moves
	now = now.Add(time.Hour)
	move(t, e, older.ID, uuid.Nil, "submit") // older's tokens enter at 11:00
	assertInvariants(t, e, repo, older.ID)

	items, err := e.Inbox(context.Background(), nil, InboxFilter{})
	require.NoError(t, err)
	require.Len(t, items, 3)
	assert.Equal(t, []uuid.UUID{older.ID, older.ID, newer.ID}, workItemCaseIDs(items))
	assert.Equal(t, "legal", items[0].Token.Node, "inside a case, by EnteredSeq-ordered entry")

	page, err := e.Inbox(context.Background(), nil, InboxFilter{Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, older.ID, page[0].Case.ID)
}

// TestCancelToken_SiblingLiveInsideANestedBlockCounts cancels the outer
// "finance" branch while the "legal" branch is still running inside its
// inner block: the inner scope is that branch, so finance is not the last.
func TestCancelToken_SiblingLiveInsideANestedBlockCounts(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c := openCase(t, e, "contract-nested")
	move(t, e, c.ID, uuid.Nil, "submit")
	active := activeTokens(t, e, c.ID)

	_, err := e.CancelToken(context.Background(), nil, CancelTokenInput{CaseID: c.ID, TokenID: active["finance"].ID, Actor: Actor{ID: "u1"}, Reason: "not needed"})
	require.NoError(t, err)
	assertInvariants(t, e, repo, c.ID)

	move(t, e, c.ID, active["contracts"].ID, "clear")
	res := move(t, e, c.ID, active["compliance"].ID, "clear")
	assertInvariants(t, e, repo, c.ID)
	require.Len(t, res.Joined, 2, "the inner join fires, then the outer one counts the cancelled branch")
	assert.Equal(t, StatusClosed, res.Case.Status)
}

func TestRecordFailure_NamesTheBranchToken(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	legal := active["legal"]

	evt, err := e.RecordFailure(context.Background(), nil, FailureInput{
		CaseID: c.ID, TokenID: legal.ID, Actor: Actor{ID: "u1"}, Action: "clear", Kind: FailureGuardRejected,
	})
	require.NoError(t, err)
	assert.Equal(t, legal.ID, evt.TokenID)
	assert.Equal(t, legal.ScopeID, evt.ScopeID)
	assert.Equal(t, "legal", evt.FromState)
	assertInvariants(t, e, repo, c.ID)

	_, err = e.RecordFailure(context.Background(), nil, FailureInput{
		CaseID: c.ID, TokenID: uuid.New(), Actor: Actor{ID: "u1"}, Kind: FailureGuardRejected,
	})
	require.ErrorIs(t, err, ErrTokenNotFound)

	// Without a token the record is case-level, as before: the root token.
	evt, err = e.RecordFailure(context.Background(), nil, FailureInput{CaseID: c.ID, Actor: Actor{ID: "u1"}, Kind: FailureConflict})
	require.NoError(t, err)
	history, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.ID, history[len(history)-1].TokenID)
	assert.Equal(t, evt.Seq, history[len(history)-1].Seq)
}
