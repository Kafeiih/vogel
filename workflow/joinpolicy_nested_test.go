package workflow

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Sequences the independent verification of the join policies ran: cancellations
// around votes, and policy joins nested inside a branch of a policy block.

func tally(r JoinResult) string {
	return fmt.Sprintf("v=%s rej=%d app=%d un=%d", r.Verdict, r.Count(OutcomeRejected), r.Count(OutcomeApproved), r.Count(OutcomeUnanswered))
}

// S3 literal: Ana, Juan, Pedro, Maria, 3 rounds, resubmission, event order.

func TestJoinPolicy_CancelSequences(t *testing.T) {
	cancel := func(r *round, node string) (*MoveResult, error) {
		res, err := r.e.CancelToken(context.Background(), nil, CancelTokenInput{CaseID: r.c.ID, TokenID: r.active[node].ID, Actor: Actor{ID: "boss"}, Reason: "out"})
		assertInvariants(r.t, r.e, r.repo, r.c.ID)
		return res, err
	}
	defs := map[string]Definition{"all": approvalDefinition(JoinAllApprove), "any": approvalDefinition(JoinAnyApprove), "collect": collectDefinition()}
	for name, d := range defs {
		t.Run(name+"/two cancelled then one vote approves", func(t *testing.T) {
			r := openRound(t, Config{}, d)
			_, err := cancel(r, "juan")
			require.NoError(t, err)
			_, err = cancel(r, "pedro")
			require.NoError(t, err)
			_, err = cancel(r, "maria")
			require.ErrorIs(t, err, ErrLastBranch)
			r.decide("maria", "approve", "")
			rs, err := r.e.JoinResults(context.Background(), nil, r.c.ID)
			require.NoError(t, err)
			require.Len(t, rs, 1)
			want := "v=approved rej=0 app=1 un=2"
			if name == "collect" {
				want = "v= rej=0 app=1 un=2"
			}
			assert.Equal(t, want, tally(rs[0]))
			assert.Len(t, rs[0].Branches, 3)
			assert.Equal(t, "publish", r.stored().State)
		})
		t.Run(name+"/vote then cancel closes round", func(t *testing.T) {
			r := openRound(t, Config{}, d)
			r.decide("juan", "reject", "x")
			if name == "all" {
				assert.Equal(t, "submit", r.stored().State)
				return
			}
			_, err := cancel(r, "pedro")
			require.NoError(t, err)
			res, err := cancel(r, "maria")
			require.NoError(t, err)
			assert.NotEmpty(t, res.Joined, "cancel fired the join")
			rs, _ := r.e.JoinResults(context.Background(), nil, r.c.ID)
			require.Len(t, rs, 1)
			if name == "any" {
				assert.Equal(t, "v=rejected rej=1 app=0 un=2", tally(rs[0]))
				assert.Equal(t, "submit", r.stored().State)
			} else {
				assert.Equal(t, "v= rej=1 app=0 un=2", tally(rs[0]))
				assert.Equal(t, "publish", r.stored().State)
			}
			assert.Equal(t, res.Joined[0].Verdict, rs[0].Verdict)
		})
		t.Run(name+"/case cancel mid round", func(t *testing.T) {
			r := openRound(t, Config{}, d)
			if name == "all" {
				r.decide("juan", "approve", "")
			} else {
				r.decide("juan", "reject", "x")
			}
			_, err := r.e.Cancel(context.Background(), nil, CancelInput{CaseID: r.c.ID, Actor: Actor{ID: "boss"}, Reason: "stop"})
			require.NoError(t, err)
			assertInvariants(t, r.e, r.repo, r.c.ID)
			rs, err := r.e.JoinResults(context.Background(), nil, r.c.ID)
			require.NoError(t, err)
			assert.Empty(t, rs)
		})
	}
}

func nestedPolicyDefinition(outer, inner JoinPolicy) Definition {
	d := Definition{
		Name: "nested-" + string(outer) + "-" + string(inner), Version: 1,
		Nodes: []Node{
			{ID: "submit", Start: true},
			{ID: "fork", Kind: NodeParallel, Join: "vote"},
			{ID: "a"},
			{ID: "b0"},
			{ID: "ifork", Kind: NodeParallel, Join: "ivote"},
			{ID: "b1"}, {ID: "b2"},
			{ID: "ivote", Kind: NodeJoin, JoinPolicy: inner},
			{ID: "b3"},
			{ID: "cfork", Kind: NodeParallel, Join: "cjoin"},
			{ID: "c1"}, {ID: "c2"},
			{ID: "cjoin", Kind: NodeJoin},
			{ID: "c3"},
			{ID: "vote", Kind: NodeJoin, JoinPolicy: outer},
			{ID: "route", Kind: NodeDecision},
			{ID: "publish"},
			{ID: "done", Terminal: true},
		},
		Transitions: []Transition{
			{From: "submit", To: "fork", Action: "send"},
			{From: "fork", To: "a", Action: "to-a"},
			{From: "fork", To: "b0", Action: "to-b"},
			{From: "fork", To: "cfork", Action: "to-c"},
			{From: "a", To: "vote", Action: "approve", Outcome: OutcomeApproved},
			{From: "a", To: "vote", Action: "reject", Outcome: OutcomeRejected, Comment: CommentRequired},
			{From: "b0", To: "ifork", Action: "go"},
			{From: "ifork", To: "b1", Action: "to-b1"},
			{From: "ifork", To: "b2", Action: "to-b2"},
			{From: "b1", To: "ivote", Action: "approve", Outcome: OutcomeApproved},
			{From: "b1", To: "ivote", Action: "reject", Outcome: OutcomeRejected, Comment: CommentRequired},
			{From: "b2", To: "ivote", Action: "approve", Outcome: OutcomeApproved},
			{From: "b2", To: "ivote", Action: "reject", Outcome: OutcomeRejected, Comment: CommentRequired},
			{From: "b3", To: "vote", Action: "approve", Outcome: OutcomeApproved},
			{From: "b3", To: "vote", Action: "reject", Outcome: OutcomeRejected, Comment: CommentRequired},
			{From: "cfork", To: "c1", Action: "to-c1"},
			{From: "cfork", To: "c2", Action: "to-c2"},
			{From: "c1", To: "cjoin", Action: "ok"},
			{From: "c2", To: "cjoin", Action: "ok"},
			{From: "cjoin", To: "c3", Action: "merged"},
			{From: "c3", To: "vote", Action: "approve", Outcome: OutcomeApproved},
			{From: "c3", To: "vote", Action: "reject", Outcome: OutcomeRejected, Comment: CommentRequired},
			{From: "vote", To: "route", Action: "tallied"},
			{From: "route", To: "submit", Action: "back", Guard: "rejected"},
			{From: "route", To: "publish", Action: "onward"},
			{From: "publish", To: "done", Action: "finish"},
		},
	}
	if inner == JoinCollect {
		d.Transitions = append(d.Transitions, Transition{From: "ivote", To: "b3", Action: "tallied"})
	} else {
		d.Nodes = append(d.Nodes, Node{ID: "iroute", Kind: NodeDecision})
		d.Transitions = append(d.Transitions,
			Transition{From: "ivote", To: "iroute", Action: "tallied"},
			Transition{From: "iroute", To: "b0", Action: "back", Guard: "rejected"},
			Transition{From: "iroute", To: "b3", Action: "onward"})
	}
	if outer == JoinCollect {
		for i := range d.Transitions {
			if d.Transitions[i].From == "vote" {
				d.Transitions[i].To = "publish"
			}
		}
		var nodes []Node
		for _, n := range d.Nodes {
			if n.ID != "route" {
				nodes = append(nodes, n)
			}
		}
		d.Nodes = nodes
		var trs []Transition
		for _, tr := range d.Transitions {
			if tr.From != "route" {
				trs = append(trs, tr)
			}
		}
		d.Transitions = append(trs, Transition{From: "publish", To: "submit", Action: "redo"})
	}
	return d
}

func TestJoinPolicy_NestedEarlyCloses(t *testing.T) {
	ctx := context.Background()
	for _, outer := range []JoinPolicy{JoinAllApprove, JoinAnyApprove} {
		for _, inner := range []JoinPolicy{JoinAllApprove, JoinAnyApprove, JoinCollect} {
			t.Run(string(outer)+"/"+string(inner), func(t *testing.T) {
				d := nestedPolicyDefinition(outer, inner)
				require.NoError(t, d.Validate())
				r := openRound(t, Config{}, d)
				at := func() map[string]Token { return activeTokens(t, r.e, r.c.ID) }
				mv := func(node, action, comment string) {
					t.Helper()
					_, err := r.e.Move(ctx, nil, MoveInput{CaseID: r.c.ID, TokenID: at()[node].ID, Action: action, ActorID: node, Comment: comment})
					require.NoError(t, err)
					assertInvariants(t, r.e, r.repo, r.c.ID)
				}
				mv("b0", "go", "")
				// One inner vote that does not close under the inner policy.
				if inner == JoinAllApprove || inner == JoinCollect {
					mv("b1", "approve", "")
				} else {
					mv("b1", "reject", "no")
				}
				mv("c1", "ok", "")
				// Outer closes by branch a while inner and plain nested blocks are open.
				if outer == JoinAllApprove {
					mv("a", "reject", "no")
				} else {
					mv("a", "approve", "")
				}
				scs, _ := r.repo.ListScopes(ctx, nil, r.c.ID)
				count := map[ScopeStatus]int{}
				for _, s := range scs {
					count[s.Status]++
				}
				assert.Equal(t, 2, count[ScopeCancelled], "%+v", scs)
				rs, err := r.e.JoinResults(ctx, nil, r.c.ID)
				require.NoError(t, err)
				require.Len(t, rs, 1)
				assert.Len(t, rs[0].Branches, 3)
				var fork *Scope
				for i := range scs {
					if scs[i].ID == rs[0].Scope {
						fork = &scs[i]
					}
				}
				require.NotNil(t, fork)
				assert.Equal(t, fork.Expected, len(rs[0].Branches))
				assert.Equal(t, 2, rs[0].Count(OutcomeUnanswered))
				toks, _ := r.repo.ListTokens(ctx, nil, r.c.ID)
				for _, tk := range toks {
					if tk.Node == "b1" || tk.Node == "b2" || tk.Node == "c1" || tk.Node == "c2" || tk.Node == "ivote" || tk.Node == "cjoin" {
						assert.Equal(t, TokenCancelled, tk.Status, tk.Node)
					}
				}
				// All votes open again next round.
				if outer == JoinAllApprove {
					assert.Equal(t, "submit", r.stored().State)
					mv("submit", "send", "")
					assert.Len(t, at(), 4)
				} else {
					assert.Equal(t, "publish", r.stored().State)
				}
			})
		}
	}
}

// Inner policy rejects, goes back inside the branch, votes again, then outer.
func TestJoinPolicy_InnerRejectThenOuter(t *testing.T) {
	ctx := context.Background()
	d := nestedPolicyDefinition(JoinAllApprove, JoinAllApprove)
	r := openRound(t, Config{}, d)
	at := func() map[string]Token { return activeTokens(t, r.e, r.c.ID) }
	mv := func(node, action, comment string) *MoveResult {
		t.Helper()
		res, err := r.e.MoveDetailed(ctx, nil, MoveInput{CaseID: r.c.ID, TokenID: at()[node].ID, Action: action, ActorID: node, Comment: comment})
		require.NoError(t, err)
		assertInvariants(t, r.e, r.repo, r.c.ID)
		return res
	}
	mv("b0", "go", "")
	mv("b1", "reject", "inner no")
	assert.Contains(t, at(), "b0")
	mv("a", "approve", "")
	mv("b0", "go", "")
	mv("b1", "approve", "")
	mv("b2", "approve", "")
	mv("b3", "approve", "")
	mv("c1", "ok", "")
	mv("c2", "ok", "")
	mv("c3", "approve", "")
	assert.Equal(t, "publish", r.stored().State)
	rs, err := r.e.JoinResults(ctx, nil, r.c.ID)
	require.NoError(t, err)
	require.Len(t, rs, 3)
	assert.Equal(t, "ivote", rs[0].JoinNode)
	assert.Equal(t, "v=rejected rej=1 app=0 un=1", tally(rs[0]))
	assert.Equal(t, "ivote", rs[1].JoinNode)
	assert.Equal(t, "v=approved rej=0 app=2 un=0", tally(rs[1]))
	assert.Equal(t, "vote", rs[2].JoinNode)
	assert.Equal(t, "v=approved rej=0 app=3 un=0", tally(rs[2]))
}

// Cancel inside a nested block, then outer close; nested cancelled token must
// produce exactly one branch row for that branch.
func TestJoinPolicy_NestedManualCancelThenOuterClose(t *testing.T) {
	ctx := context.Background()
	d := nestedPolicyDefinition(JoinAllApprove, JoinCollect)
	r := openRound(t, Config{}, d)
	at := func() map[string]Token { return activeTokens(t, r.e, r.c.ID) }
	mv := func(node, action, comment string) {
		t.Helper()
		_, err := r.e.Move(ctx, nil, MoveInput{CaseID: r.c.ID, TokenID: at()[node].ID, Action: action, ActorID: node, Comment: comment})
		require.NoError(t, err)
		assertInvariants(t, r.e, r.repo, r.c.ID)
	}
	mv("b0", "go", "")
	_, err := r.e.CancelToken(ctx, nil, CancelTokenInput{CaseID: r.c.ID, TokenID: at()["c1"].ID, Actor: Actor{ID: "boss"}, Reason: "x"})
	require.NoError(t, err)
	assertInvariants(t, r.e, r.repo, r.c.ID)
	_, err = r.e.CancelToken(ctx, nil, CancelTokenInput{CaseID: r.c.ID, TokenID: at()["b1"].ID, Actor: Actor{ID: "boss"}, Reason: "x"})
	require.NoError(t, err)
	assertInvariants(t, r.e, r.repo, r.c.ID)
	mv("a", "reject", "no")
	rs, err := r.e.JoinResults(ctx, nil, r.c.ID)
	require.NoError(t, err)
	require.Len(t, rs, 1)
	assert.Equal(t, "v=rejected rej=1 app=0 un=2", tally(rs[0]))
	seen := map[uuid.UUID]bool{}
	for _, b := range rs[0].Branches {
		assert.False(t, seen[b.Token])
		seen[b.Token] = true
	}
}
