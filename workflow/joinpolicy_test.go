package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// approvalDefinition returns the process the join policies exist for: "submit"
// sends a case to three approvers in parallel, their votes meet at the "vote"
// join under policy, and the "route" decision sends the case back to "submit"
// on a rejected verdict or on to "publish" otherwise. rejected is the guard
// registered as workflow.JoinRejected.
func approvalDefinition(policy JoinPolicy) Definition {
	d := Definition{
		Name:    "approval-" + string(policy),
		Version: 1,
		Nodes: []Node{
			{ID: "submit", Start: true, Eligible: ByPosition("requester")},
			{ID: "fork", Kind: NodeParallel, Join: "vote"},
			{ID: "juan", Eligible: ByPosition("approver")},
			{ID: "pedro", Eligible: ByPosition("approver")},
			{ID: "maria", Eligible: ByPosition("approver")},
			{ID: "vote", Kind: NodeJoin, JoinPolicy: policy},
			{ID: "route", Kind: NodeDecision},
			{ID: "publish", Eligible: ByPosition("clerk")},
			{ID: "done", Terminal: true},
		},
		Transitions: []Transition{
			{From: "submit", To: "fork", Action: "send"},
			{From: "fork", To: "juan", Action: "to-juan"},
			{From: "fork", To: "pedro", Action: "to-pedro"},
			{From: "fork", To: "maria", Action: "to-maria"},
			{From: "juan", To: "vote", Action: "approve", Outcome: OutcomeApproved},
			{From: "juan", To: "vote", Action: "reject", Outcome: OutcomeRejected, Comment: CommentRequired},
			{From: "pedro", To: "vote", Action: "approve", Outcome: OutcomeApproved},
			{From: "pedro", To: "vote", Action: "reject", Outcome: OutcomeRejected, Comment: CommentRequired},
			{From: "maria", To: "vote", Action: "approve", Outcome: OutcomeApproved},
			{From: "maria", To: "vote", Action: "reject", Outcome: OutcomeRejected, Comment: CommentRequired},
			{From: "vote", To: "route", Action: "tallied"},
			{From: "route", To: "submit", Action: "back", Guard: "rejected"},
			{From: "route", To: "publish", Action: "onward"},
			{From: "publish", To: "done", Action: "finish"},
		},
	}
	return d
}

// collectDefinition is approvalDefinition under JoinCollect: the join reaches
// the task where a person reads the tally and decides, and rejections carry an
// optional comment (A4).
func collectDefinition() Definition {
	d := approvalDefinition(JoinCollect)
	var nodes []Node
	for _, n := range d.Nodes {
		if n.ID == "route" {
			continue
		}
		nodes = append(nodes, n)
	}
	d.Nodes = nodes
	var trs []Transition
	for _, tr := range d.Transitions {
		switch {
		case tr.From == "route":
			continue
		case tr.From == "vote":
			tr.To = "publish"
		case tr.Outcome == OutcomeRejected:
			tr.Comment = CommentOptional
		}
		trs = append(trs, tr)
	}
	trs = append(trs, Transition{From: "publish", To: "submit", Action: "redo"})
	d.Transitions = trs
	return d
}

func TestValidate_ApprovalDefinitionsAreValid(t *testing.T) {
	for _, policy := range []JoinPolicy{JoinAllApprove, JoinAnyApprove} {
		assert.NoError(t, approvalDefinition(policy).Validate(), policy)
	}
	assert.NoError(t, collectDefinition().Validate())
}

// S1

func TestValidate_JoinPolicyOnlyOnJoin(t *testing.T) {
	t.Run("on a task", func(t *testing.T) {
		d := approvalDefinition(JoinAllApprove)
		d.Nodes[0].JoinPolicy = JoinAllApprove
		err := d.Validate()
		require.ErrorIs(t, err, ErrInvalidDefinition)
		assert.Contains(t, err.Error(), `node "submit": only a join may declare a join policy`)
	})
	t.Run("on a split", func(t *testing.T) {
		d := approvalDefinition(JoinAllApprove)
		d.Nodes[1].JoinPolicy = JoinCollect
		err := d.Validate()
		require.ErrorIs(t, err, ErrInvalidDefinition)
		assert.Contains(t, err.Error(), `node "fork": only a join may declare a join policy`)
	})
	t.Run("unknown value", func(t *testing.T) {
		d := approvalDefinition("majority")
		err := d.Validate()
		require.ErrorIs(t, err, ErrInvalidDefinition)
		assert.Contains(t, err.Error(), `join node "vote": invalid join policy "majority"`)
	})
}

func TestValidate_RejectsPolicyOnInclusiveJoin(t *testing.T) {
	d := inclusiveDefinition()
	for i := range d.Nodes {
		if d.Nodes[i].ID == "merge" {
			d.Nodes[i].JoinPolicy = JoinCollect
		}
	}
	err := d.Validate()
	require.ErrorIs(t, err, ErrInvalidDefinition)
	assert.Contains(t, err.Error(), `join node "merge": a join policy needs a parallel split, but inclusive split "pick" names it`)
}

// S2

func TestValidate_PolicyJoinRequiresOutcomeFromTask(t *testing.T) {
	t.Run("missing outcome", func(t *testing.T) {
		d := approvalDefinition(JoinAllApprove)
		for i := range d.Transitions {
			if d.Transitions[i].From == "juan" && d.Transitions[i].Action == "approve" {
				d.Transitions[i].Outcome = ""
			}
		}
		err := d.Validate()
		require.ErrorIs(t, err, ErrInvalidDefinition)
		assert.Contains(t, err.Error(), `must declare an outcome: it enters join "vote", which has a policy`)
	})
	t.Run("from a split", func(t *testing.T) {
		d := approvalDefinition(JoinAllApprove)
		d.Transitions = append(d.Transitions, Transition{From: "fork", To: "vote", Action: "skip", Outcome: OutcomeApproved})
		err := d.Validate()
		require.ErrorIs(t, err, ErrInvalidDefinition)
		assert.Contains(t, err.Error(), `must leave a task node: it enters join "vote", which has a policy, from parallel split "fork"`)
	})
	t.Run("unknown or unanswered outcome", func(t *testing.T) {
		for _, outcome := range []Outcome{"maybe", OutcomeUnanswered} {
			d := approvalDefinition(JoinCollect)
			d.Transitions[4].Outcome = outcome
			err := d.Validate()
			require.ErrorIs(t, err, ErrInvalidDefinition)
			assert.Contains(t, err.Error(), `invalid outcome "`+string(outcome)+`"`)
		}
	})
}

func TestValidate_RejectsOutcomeOutsidePolicyJoin(t *testing.T) {
	t.Run("on an ordinary transition", func(t *testing.T) {
		d := approvalDefinition(JoinAllApprove)
		d.Transitions[0].Outcome = OutcomeApproved
		err := d.Validate()
		require.ErrorIs(t, err, ErrInvalidDefinition)
		assert.Contains(t, err.Error(), `must not declare an outcome: it does not enter a join with a policy`)
	})
	t.Run("entering a join without a policy", func(t *testing.T) {
		d := parallelDefinition()
		for i := range d.Transitions {
			if d.Transitions[i].To == "merge" {
				d.Transitions[i].Outcome = OutcomeApproved
			}
		}
		err := d.Validate()
		require.ErrorIs(t, err, ErrInvalidDefinition)
		assert.Contains(t, err.Error(), `must not declare an outcome: it does not enter a join with a policy`)
	})
}

func TestValidate_RejectedRequiresComment(t *testing.T) {
	for _, policy := range []JoinPolicy{JoinAllApprove, JoinAnyApprove} {
		t.Run(string(policy), func(t *testing.T) {
			d := approvalDefinition(policy)
			for i := range d.Transitions {
				if d.Transitions[i].From == "pedro" && d.Transitions[i].Action == "reject" {
					d.Transitions[i].Comment = CommentOptional
				}
			}
			err := d.Validate()
			require.ErrorIs(t, err, ErrInvalidDefinition)
			assert.Contains(t, err.Error(), `a rejected outcome under `+string(policy)+` must require a comment`)
		})
	}
	t.Run("collect does not", func(t *testing.T) {
		assert.NoError(t, collectDefinition().Validate())
	})
}

// S10

// policyEngine returns an engine over a fresh fake repository with defs
// registered.
func policyEngine(t *testing.T, cfg Config, defs ...Definition) (*Engine, *fakeRepository) {
	t.Helper()
	repo := newFakeRepository()
	e := New(repo, cfg, testLogger())
	for _, d := range defs {
		require.NoError(t, e.Register(d))
	}
	return e, repo
}

func TestOutcome_RecordedOnTokenAndWaitingEvent(t *testing.T) {
	d := collectDefinition()
	e, repo := policyEngine(t, Config{}, d)
	c := openCase(t, e, d.Name)
	move(t, e, c.ID, uuid.Nil, "send")
	active := activeTokens(t, e, c.ID)

	res, err := e.MoveDetailed(context.Background(), nil, MoveInput{
		CaseID: c.ID, TokenID: active["pedro"].ID, Action: "reject", ActorID: "pedro", Comment: "falta el certificado",
	})
	require.NoError(t, err)
	assertInvariants(t, e, repo, c.ID)

	require.Equal(t, []EventKind{EventMoved, EventWaiting}, eventKinds(res.Events))
	assert.Equal(t, "falta el certificado", res.Events[0].Comment, "the comment stays in the move event")
	var detail WaitingDetail
	require.NoError(t, json.Unmarshal(res.Events[1].Detail, &detail))
	assert.Equal(t, OutcomeRejected, detail.Outcome)

	tokens, err := e.Tokens(context.Background(), nil, c.ID)
	require.NoError(t, err)
	for _, tok := range tokens {
		if tok.ID == active["pedro"].ID {
			assert.Equal(t, TokenWaiting, tok.Status)
			assert.Equal(t, OutcomeRejected, tok.Outcome)
		} else {
			assert.Empty(t, tok.Outcome, "token at %s has not answered", tok.Node)
		}
	}
}

func TestWaiting_WithoutPolicyHasNoOutcome(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	res := move(t, e, c.ID, active["legal"].ID, "clear")
	assert.JSONEq(t, `{}`, string(res.Events[1].Detail))
}

// round is a case sent to its approvers: the engine, the case, and the branch
// tokens by node as they were when the round opened.
type round struct {
	t      *testing.T
	e      *Engine
	repo   *fakeRepository
	c      *Case
	active map[string]Token
}

// openRound registers d with the "rejected" guard, opens a case and sends it.
func openRound(t *testing.T, cfg Config, d Definition) *round {
	t.Helper()
	e, repo := policyEngine(t, cfg, d)
	require.NoError(t, e.RegisterGuard("rejected", JoinRejected))
	c := openCase(t, e, d.Name)
	move(t, e, c.ID, uuid.Nil, "send")
	return &round{t: t, e: e, repo: repo, c: c, active: activeTokens(t, e, c.ID)}
}

// decide moves node's branch through action as the person named like the node,
// checking the invariants after the call.
func (r *round) decide(node, action, comment string) *MoveResult {
	r.t.Helper()
	res, err := r.e.MoveDetailed(context.Background(), nil, MoveInput{
		CaseID: r.c.ID, TokenID: r.active[node].ID, Action: action, ActorID: node, Comment: comment,
	})
	require.NoError(r.t, err)
	assertInvariants(r.t, r.e, r.repo, r.c.ID)
	return res
}

func (r *round) stored() *Case {
	r.t.Helper()
	got, err := r.repo.GetByID(context.Background(), nil, r.c.ID)
	require.NoError(r.t, err)
	return got
}

func (r *round) tokenStatus(node string) TokenStatus {
	r.t.Helper()
	tokens, err := r.e.Tokens(context.Background(), nil, r.c.ID)
	require.NoError(r.t, err)
	for _, tok := range tokens {
		if tok.ID == r.active[node].ID {
			return tok.Status
		}
	}
	r.t.Fatalf("no token for node %s", node)
	return ""
}

// outcomes renders a join record's branches keyed by their node in the round.
func (r *round) outcomes(j JoinRecord) map[string]Outcome {
	r.t.Helper()
	out := make(map[string]Outcome, len(j.Branches))
	for _, b := range j.Branches {
		for node, tok := range r.active {
			if tok.ID == b.Token {
				out[node] = b.Outcome
			}
		}
	}
	return out
}

func countEventKind(events []Event, kind EventKind) int {
	n := 0
	for i := range events {
		if events[i].Kind == kind {
			n++
		}
	}
	return n
}

func cancelReason(t *testing.T, ev Event) string {
	t.Helper()
	var detail CancelDetail
	require.NoError(t, json.Unmarshal(ev.Detail, &detail))
	return detail.Reason
}

// S3

func TestAllApprove_FirstRejectionCancelsPendingAndGoesBack(t *testing.T) {
	r := openRound(t, Config{}, approvalDefinition(JoinAllApprove))
	r.decide("juan", "approve", "")
	res := r.decide("pedro", "reject", "falta el certificado")

	require.Equal(t, StatusOpen, res.Case.Status, "the case stays open")
	assert.Equal(t, "submit", res.Case.State, "the case returns to the requester")
	assert.Equal(t, TokenCancelled, r.tokenStatus("maria"), "the pending approver is cancelled")
	assert.Equal(t, TokenConsumed, r.tokenStatus("juan"))
	assert.Equal(t, TokenConsumed, r.tokenStatus("pedro"))

	require.Len(t, res.Joined, 1)
	j := res.Joined[0]
	assert.Equal(t, OutcomeRejected, j.Verdict)
	assert.Equal(t, map[string]Outcome{"juan": OutcomeApproved, "pedro": OutcomeRejected, "maria": OutcomeUnanswered}, r.outcomes(j))
	assert.Equal(t, "falta el certificado", res.Events[0].Comment, "the reason stays in the move event")
}

func TestAllApprove_AllApprovedAdvances(t *testing.T) {
	r := openRound(t, Config{}, approvalDefinition(JoinAllApprove))
	assert.Empty(t, r.decide("juan", "approve", "").Joined)
	assert.Empty(t, r.decide("pedro", "approve", "").Joined)
	res := r.decide("maria", "approve", "")

	assert.Equal(t, "publish", res.Case.State)
	require.Len(t, res.Joined, 1)
	assert.Equal(t, OutcomeApproved, res.Joined[0].Verdict)
	assert.Equal(t, map[string]Outcome{"juan": OutcomeApproved, "pedro": OutcomeApproved, "maria": OutcomeApproved}, r.outcomes(res.Joined[0]))
	assert.Zero(t, countEventKind(res.Events, EventTokenCancelled))
}

func TestAllApprove_RejectionWithoutCommentFails(t *testing.T) {
	r := openRound(t, Config{}, approvalDefinition(JoinAllApprove))
	before := r.stored()
	_, err := r.e.Move(context.Background(), nil, MoveInput{CaseID: r.c.ID, TokenID: r.active["pedro"].ID, Action: "reject", ActorID: "pedro"})
	require.ErrorIs(t, err, ErrCommentRequired)
	assert.Equal(t, before.LastSeq, r.stored().LastSeq, "nothing is persisted")
	assert.Equal(t, TokenActive, r.tokenStatus("pedro"))
}

// S4

func TestAnyApprove_FirstApprovalCancelsPendingAndAdvances(t *testing.T) {
	r := openRound(t, Config{}, approvalDefinition(JoinAnyApprove))
	res := r.decide("pedro", "approve", "")

	assert.Equal(t, "publish", res.Case.State)
	require.Len(t, res.Joined, 1)
	assert.Equal(t, OutcomeApproved, res.Joined[0].Verdict)
	assert.Equal(t, map[string]Outcome{"pedro": OutcomeApproved, "juan": OutcomeUnanswered, "maria": OutcomeUnanswered}, r.outcomes(res.Joined[0]))
	assert.Equal(t, TokenCancelled, r.tokenStatus("juan"))
	assert.Equal(t, TokenCancelled, r.tokenStatus("maria"))
}

func TestAnyApprove_RejectionKeepsWaiting(t *testing.T) {
	r := openRound(t, Config{}, approvalDefinition(JoinAnyApprove))
	res := r.decide("juan", "reject", "no")

	assert.Empty(t, res.Joined, "a rejection does not close the round while a branch is pending")
	assert.Equal(t, []EventKind{EventMoved, EventWaiting}, eventKinds(res.Events))
	assert.Equal(t, TokenActive, r.tokenStatus("pedro"))
	assert.Equal(t, TokenActive, r.tokenStatus("maria"))
}

func TestAnyApprove_LastBranchApprovesReportsTwoRejections(t *testing.T) {
	r := openRound(t, Config{}, approvalDefinition(JoinAnyApprove))
	r.decide("juan", "reject", "no")
	r.decide("pedro", "reject", "tampoco")
	res := r.decide("maria", "approve", "")

	assert.Equal(t, "publish", res.Case.State)
	require.Len(t, res.Joined, 1)
	assert.Equal(t, OutcomeApproved, res.Joined[0].Verdict)
	assert.Equal(t, map[string]Outcome{"juan": OutcomeRejected, "pedro": OutcomeRejected, "maria": OutcomeApproved}, r.outcomes(res.Joined[0]),
		"the next step receives rejected 2, approved 1")
}

func TestAnyApprove_AllRejectedGoesBack(t *testing.T) {
	r := openRound(t, Config{}, approvalDefinition(JoinAnyApprove))
	r.decide("juan", "reject", "no")
	r.decide("pedro", "reject", "no")
	res := r.decide("maria", "reject", "no")

	assert.Equal(t, StatusOpen, res.Case.Status)
	assert.Equal(t, "submit", res.Case.State, "nobody approved, so the case goes back")
	require.Len(t, res.Joined, 1)
	assert.Equal(t, OutcomeRejected, res.Joined[0].Verdict)
}

// S5

func TestCollect_WaitsForEveryBranchAndHasNoVerdict(t *testing.T) {
	r := openRound(t, Config{}, collectDefinition())
	assert.Empty(t, r.decide("juan", "reject", "").Joined, "a rejection does not close the round")
	assert.Empty(t, r.decide("pedro", "approve", "").Joined, "an approval does not close the round")
	res := r.decide("maria", "approve", "")

	assert.Equal(t, "publish", res.Case.State, "a person decides on the task after the join")
	require.Len(t, res.Joined, 1)
	assert.Empty(t, res.Joined[0].Verdict)
	assert.Equal(t, map[string]Outcome{"juan": OutcomeRejected, "pedro": OutcomeApproved, "maria": OutcomeApproved}, r.outcomes(res.Joined[0]))
	assert.Zero(t, countEventKind(res.Events, EventTokenCancelled), "nothing is cancelled")
}

func TestValidate_CollectJoinMustReachTask(t *testing.T) {
	retarget := func(to string, extra ...Node) Definition {
		d := collectDefinition()
		d.Nodes = append(d.Nodes, extra...)
		for i := range d.Transitions {
			if d.Transitions[i].From == "vote" {
				d.Transitions[i].To = to
			}
		}
		return d
	}
	t.Run("a decision node", func(t *testing.T) {
		d := retarget("route", Node{ID: "route", Kind: NodeDecision})
		d.Transitions = append(d.Transitions,
			Transition{From: "route", To: "submit", Action: "back", Guard: "rejected"},
			Transition{From: "route", To: "publish", Action: "onward"})
		err := d.Validate()
		require.ErrorIs(t, err, ErrInvalidDefinition)
		assert.Contains(t, err.Error(), `join node "vote": under collect its outgoing transition must reach a task node`)
	})
	t.Run("a terminal node", func(t *testing.T) {
		err := retarget("done").Validate()
		require.ErrorIs(t, err, ErrInvalidDefinition)
		assert.Contains(t, err.Error(), `join node "vote": under collect its outgoing transition must reach a task node`)
	})
}

// S7 (the Validate rule; routing follows in the S3 and S4 tests above)

func TestValidate_VerdictJoinMustReachDecision(t *testing.T) {
	for _, policy := range []JoinPolicy{JoinAllApprove, JoinAnyApprove} {
		t.Run(string(policy), func(t *testing.T) {
			d := approvalDefinition(policy)
			for i := range d.Transitions {
				if d.Transitions[i].From == "vote" {
					d.Transitions[i].To = "publish"
				}
			}
			err := d.Validate()
			require.ErrorIs(t, err, ErrInvalidDefinition)
			assert.Contains(t, err.Error(), `join node "vote": under `+string(policy)+` its outgoing transition must reach a decision node, got "publish"`)
		})
	}
}

// S9

// nestedApprovalDefinition is an all_approve block whose "legal" branch runs an
// inner block before its approver: "fork2" splits into "contracts" and
// "compliance", which meet at "merge2" and continue to "legal".
func nestedApprovalDefinition() Definition {
	return Definition{
		Name: "approval-nested", Version: 1,
		Nodes: []Node{
			{ID: "submit", Start: true},
			{ID: "fork", Kind: NodeParallel, Join: "vote"},
			{ID: "fork2", Kind: NodeParallel, Join: "merge2"},
			{ID: "contracts"},
			{ID: "compliance"},
			{ID: "merge2", Kind: NodeJoin},
			{ID: "legal"},
			{ID: "finance"},
			{ID: "vote", Kind: NodeJoin, JoinPolicy: JoinAllApprove},
			{ID: "route", Kind: NodeDecision},
			{ID: "done", Terminal: true},
		},
		Transitions: []Transition{
			{From: "submit", To: "fork", Action: "send"},
			{From: "fork", To: "fork2", Action: "to-legal"},
			{From: "fork", To: "finance", Action: "to-finance"},
			{From: "fork2", To: "contracts", Action: "to-contracts"},
			{From: "fork2", To: "compliance", Action: "to-compliance"},
			{From: "contracts", To: "merge2", Action: "clear"},
			{From: "compliance", To: "merge2", Action: "clear"},
			{From: "merge2", To: "legal", Action: "cleared"},
			{From: "legal", To: "vote", Action: "approve", Outcome: OutcomeApproved},
			{From: "finance", To: "vote", Action: "approve", Outcome: OutcomeApproved},
			{From: "finance", To: "vote", Action: "reject", Outcome: OutcomeRejected, Comment: CommentRequired},
			{From: "vote", To: "route", Action: "tallied"},
			{From: "route", To: "submit", Action: "back", Guard: "rejected"},
			{From: "route", To: "done", Action: "onward"},
		},
	}
}

func TestEarlyClose_CancelsNestedScopes(t *testing.T) {
	r := openRound(t, Config{}, nestedApprovalDefinition())
	// One inner branch already waits at the inner join, the other is pending.
	r.decide("contracts", "clear", "")
	res := r.decide("finance", "reject", "sin fondos")

	require.Equal(t, "submit", res.Case.State)
	joined := res.Events[len(res.Events)-3]
	require.Equal(t, EventJoined, joined.Kind)

	scopes, err := r.repo.ListScopes(context.Background(), nil, r.c.ID)
	require.NoError(t, err)
	var inner *Scope
	for i := range scopes {
		if scopes[i].SplitNode == "fork2" {
			inner = &scopes[i]
		}
	}
	require.NotNil(t, inner)
	assert.Equal(t, ScopeCancelled, inner.Status)
	assert.Equal(t, joined.Seq, inner.ClosedSeq, "the join that closed the round closed the nested scope")

	assert.Equal(t, 2, countEventKind(res.Events, EventTokenCancelled), "the waiting and the active inner tokens")
	assert.Equal(t, TokenCancelled, r.tokenStatus("contracts"))
	assert.Equal(t, TokenCancelled, r.tokenStatus("compliance"))

	require.Len(t, res.Joined, 1)
	branches := res.Joined[0].Branches
	require.Len(t, branches, 2, "the nested block stands for one branch")
	var tally []Outcome
	for _, b := range branches {
		tally = append(tally, b.Outcome)
	}
	assert.ElementsMatch(t, []Outcome{OutcomeRejected, OutcomeUnanswered}, tally)
}

func TestEarlyClose_EventOrder(t *testing.T) {
	r := openRound(t, Config{}, approvalDefinition(JoinAllApprove))
	res := r.decide("pedro", "reject", "no")

	assert.Equal(t, []EventKind{
		EventMoved, EventWaiting, // the deciding branch
		EventTokenCancelled, EventTokenCancelled, // the pending ones
		EventJoined,
		EventMoved, EventMoved, // join -> decision -> earlier task
	}, eventKinds(res.Events))
	for _, ev := range res.Events[2:4] {
		assert.Equal(t, "join_policy:all_approve", cancelReason(t, ev))
		assert.Equal(t, ev.FromState, ev.ToState)
	}
}

func TestEarlyClose_LateSubmitIsTokenNotFound(t *testing.T) {
	r := openRound(t, Config{}, approvalDefinition(JoinAnyApprove))
	r.decide("juan", "approve", "")
	_, err := r.e.Move(context.Background(), nil, MoveInput{CaseID: r.c.ID, TokenID: r.active["maria"].ID, Action: "approve", ActorID: "maria"})
	require.ErrorIs(t, err, ErrTokenNotFound)
}

func TestEarlyClose_RemovesCancelledFromInbox(t *testing.T) {
	r := openRound(t, Config{}, approvalDefinition(JoinAnyApprove))
	filter := InboxFilter{States: []string{"juan", "pedro", "maria"}}
	items, err := r.e.Inbox(context.Background(), nil, filter)
	require.NoError(t, err)
	require.Len(t, items, 3)

	r.decide("juan", "approve", "")
	items, err = r.e.Inbox(context.Background(), nil, filter)
	require.NoError(t, err)
	assert.Empty(t, items)
}

// A3

func TestCancelToken_DoesNotCountAsVote(t *testing.T) {
	cancel := func(r *round, node string) *MoveResult {
		r.t.Helper()
		res, err := r.e.CancelToken(context.Background(), nil, CancelTokenInput{
			CaseID: r.c.ID, TokenID: r.active[node].ID, Actor: Actor{ID: "operator"}, Reason: "licencia",
		})
		require.NoError(r.t, err)
		assertInvariants(r.t, r.e, r.repo, r.c.ID)
		return res
	}
	t.Run("all_approve approves with the remaining branches", func(t *testing.T) {
		r := openRound(t, Config{}, approvalDefinition(JoinAllApprove))
		cancel(r, "pedro")
		r.decide("juan", "approve", "")
		res := r.decide("maria", "approve", "")

		assert.Equal(t, "publish", res.Case.State)
		require.Len(t, res.Joined, 1)
		assert.Equal(t, OutcomeApproved, res.Joined[0].Verdict)
		assert.Equal(t, map[string]Outcome{"juan": OutcomeApproved, "maria": OutcomeApproved, "pedro": OutcomeUnanswered}, r.outcomes(res.Joined[0]))
	})
	t.Run("any_approve rejects with the remaining branches", func(t *testing.T) {
		r := openRound(t, Config{}, approvalDefinition(JoinAnyApprove))
		cancel(r, "pedro")
		r.decide("juan", "reject", "no")
		res := r.decide("maria", "reject", "no")

		assert.Equal(t, "submit", res.Case.State)
		require.Len(t, res.Joined, 1)
		assert.Equal(t, OutcomeRejected, res.Joined[0].Verdict)
		assert.Equal(t, OutcomeUnanswered, r.outcomes(res.Joined[0])["pedro"])
	})
	t.Run("cancelling the last pending branch closes the round", func(t *testing.T) {
		r := openRound(t, Config{}, approvalDefinition(JoinAllApprove))
		r.decide("juan", "approve", "")
		r.decide("maria", "approve", "")
		res := cancel(r, "pedro")

		assert.Equal(t, "publish", res.Case.State)
		require.Len(t, res.Joined, 1)
		assert.Equal(t, OutcomeApproved, res.Joined[0].Verdict)
		for _, b := range res.Joined[0].Branches {
			if b.Token == r.active["pedro"].ID {
				assert.Equal(t, res.Events[0].Seq, b.Seq, "an unanswered branch points at its token_cancelled event")
			}
		}
	})
}

// S11

// overdueDefinition makes "maria" expire into a rejection.
func overdueDefinition(policy JoinPolicy, onExpire string) Definition {
	d := approvalDefinition(policy)
	for i := range d.Nodes {
		if d.Nodes[i].ID == "maria" {
			d.Nodes[i].Deadline = time.Hour
			d.Nodes[i].OnExpire = onExpire
		}
	}
	return d
}

func TestExpire_OnExpireOutcomeCountsAsVote(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	r := openRound(t, Config{Now: func() time.Time { return now }}, overdueDefinition(JoinAllApprove, "reject"))
	r.decide("juan", "approve", "")

	now = now.Add(2 * time.Hour)
	res, err := r.e.Expire(context.Background(), nil, ExpireInput{CaseID: r.c.ID, TokenID: r.active["maria"].ID, Actor: Actor{Kind: ActorSystem, ID: "scheduler"}})
	require.NoError(t, err)
	assertInvariants(t, r.e, r.repo, r.c.ID)

	assert.Equal(t, "submit", res.Case.State, "the expired rejection closes the round")
	require.Len(t, res.Joined, 1)
	assert.Equal(t, OutcomeRejected, res.Joined[0].Verdict)
	assert.Equal(t, OutcomeRejected, r.outcomes(res.Joined[0])["maria"])
	assert.Equal(t, OutcomeUnanswered, r.outcomes(res.Joined[0])["pedro"])
	assert.JSONEq(t, `{"cause":"expired"}`, string(res.Events[1].Detail), "the hop into the join keeps the cause")
	assert.Empty(t, res.Events[1].Comment, "an expired rejection has no reason text")
}

func TestAllApprove_WithoutOnExpireStaysPending(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	r := openRound(t, Config{Now: func() time.Time { return now }}, overdueDefinition(JoinAllApprove, ""))
	r.decide("juan", "approve", "")
	r.decide("pedro", "approve", "")

	now = now.Add(2 * time.Hour)
	res, err := r.e.Expire(context.Background(), nil, ExpireInput{CaseID: r.c.ID, TokenID: r.active["maria"].ID, Actor: Actor{Kind: ActorSystem, ID: "scheduler"}})
	require.NoError(t, err)
	assertInvariants(t, r.e, r.repo, r.c.ID)

	assert.Empty(t, res.Joined)
	assert.Equal(t, TokenActive, r.tokenStatus("maria"), "without OnExpire the token stays pending")
	assert.Equal(t, TokenWaiting, r.tokenStatus("juan"))
}

// S1

func TestJoin_EmptyPolicyKeepsCurrentBehavior(t *testing.T) {
	e, repo := tokenEngine(t, Config{}, nil)
	c, active := splitToLegalAndFinance(t, e, repo)
	move(t, e, c.ID, active["legal"].ID, "clear")
	res := move(t, e, c.ID, active["finance"].ID, "clear")
	assertInvariants(t, e, repo, c.ID)

	require.Len(t, res.Joined, 1)
	assert.Empty(t, res.Joined[0].Verdict)
	assert.Nil(t, res.Joined[0].Branches)
	for _, ev := range res.Events {
		switch ev.Kind {
		case EventWaiting:
			assert.JSONEq(t, `{}`, string(ev.Detail))
		case EventJoined:
			var raw map[string]any
			require.NoError(t, json.Unmarshal(ev.Detail, &raw))
			assert.ElementsMatch(t, []string{"scope", "join_node", "tokens"}, keysOf(raw), "no policy, verdict or branches")
		}
	}
	tokens, err := e.Tokens(context.Background(), nil, c.ID)
	require.NoError(t, err)
	for _, tok := range tokens {
		assert.Empty(t, tok.Outcome)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
