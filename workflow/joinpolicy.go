package workflow

import (
	"context"
	"errors"
	"sort"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

// JoinPolicy declares how the branches of a parallel block decide at its join
// (Node.JoinPolicy). The zero value is no policy: the join waits for every
// branch and records no outcome, tally or verdict.
type JoinPolicy string

const (
	// JoinAllApprove closes the round on the first rejection, with verdict
	// OutcomeRejected, or when every branch approved, with OutcomeApproved. A
	// rejection cancels the branches still pending.
	JoinAllApprove JoinPolicy = "all_approve"
	// JoinAnyApprove closes the round on the first approval, with verdict
	// OutcomeApproved, cancelling the branches still pending, or when every
	// branch rejected, with OutcomeRejected.
	JoinAnyApprove JoinPolicy = "any_approve"
	// JoinCollect closes the round when every branch answered, with no
	// verdict: a person reads the tally on the task after the join and decides.
	JoinCollect JoinPolicy = "collect"
)

// Valid reports whether p is empty or one of the known policies.
func (p JoinPolicy) Valid() bool {
	switch p {
	case "", JoinAllApprove, JoinAnyApprove, JoinCollect:
		return true
	default:
		return false
	}
}

// Outcome is what one branch of a policy join decided (Transition.Outcome,
// Token.Outcome), and the verdict of a round.
type Outcome string

const (
	// OutcomeApproved is an approval.
	OutcomeApproved Outcome = "approved"
	// OutcomeRejected is a rejection.
	OutcomeRejected Outcome = "rejected"
	// OutcomeUnanswered is a branch cancelled before it answered. Only a tally
	// reports it (BranchResult): a Transition cannot declare it.
	OutcomeUnanswered Outcome = "unanswered"
)

// ErrInvalidOutcome reports a token waiting at a join with a policy whose stored
// outcome is neither approved nor rejected: migration 011 was rolled back and
// applied again while it waited, it arrived before a registered definition gained
// the policy without a new version, or a custom Repository does not persist
// Token.Outcome. The call fails and persists nothing, rather than counting a vote
// it cannot read. The answer is still in the token's EventWaiting.
var ErrInvalidOutcome = errors.New("workflow: token waits at a policy join without a valid outcome")

// roundTally counts a round's branches so far: the waiting ones by answer, the
// cancelled ones, and how many branches the scope expects.
type roundTally struct {
	approved, rejected, cancelled, expected int
}

// closes reports whether a round under policy p closes at tally t, and with
// what verdict. A cancelled branch is no vote: it only stops being awaited.
func (p JoinPolicy) closes(t roundTally) (bool, Outcome) {
	waiting := t.approved + t.rejected
	complete := waiting > 0 && waiting+t.cancelled == t.expected
	switch p {
	case JoinAllApprove:
		switch {
		case t.rejected > 0:
			return true, OutcomeRejected
		case complete:
			return true, OutcomeApproved
		}
	case JoinAnyApprove:
		switch {
		case t.approved > 0:
			return true, OutcomeApproved
		case complete:
			return true, OutcomeRejected
		}
	case JoinCollect:
		return complete, ""
	}
	return false, ""
}

// BranchResult is one branch of a round in JoinDetail: its token, what it
// answered (OutcomeUnanswered when it was cancelled first) and the Seq of the
// event to read about it: the move into the join, holding the actor and the
// comment, or the EventTokenCancelled. A branch that was running a nested block
// when the round closed reports the first token cancelled inside that block.
type BranchResult struct {
	Token   uuid.UUID `json:"token"`
	Outcome Outcome   `json:"outcome"`
	Seq     int64     `json:"seq"`
}

// JoinBranch is one branch of a JoinResult.
type JoinBranch struct {
	BranchResult
}

// JoinResult is one closed round of a join with a policy: which join, under
// what policy, its verdict (empty under JoinCollect), the Seq of its
// EventJoined and every branch, oldest answer first.
type JoinResult struct {
	Scope    uuid.UUID
	JoinNode string
	Policy   JoinPolicy
	Verdict  Outcome
	Seq      int64
	Branches []JoinBranch
}

// Count returns how many branches of the round reported outcome.
func (r JoinResult) Count(outcome Outcome) int {
	n := 0
	for i := range r.Branches {
		if r.Branches[i].Outcome == outcome {
			n++
		}
	}
	return n
}

// JoinRejected is a GuardFunc for the decision node after a join with a verdict
// policy: it passes when the round that just closed was rejected, so the
// decision's guarded route is the way back and its unguarded default the way
// forward. Register it under any name. It reads Case.JoinResult, so where no
// round closed in the current call it reports false.
func JoinRejected(_ context.Context, _ pgxtx.DBTX, c Case) (bool, error) {
	return c.JoinResult != nil && c.JoinResult.Verdict == OutcomeRejected, nil
}

// closeBranches finishes a round under policy: it cancels what is still pending
// inside scope (its active tokens, and the live tokens of every scope still
// open inside it, in token order), and returns one BranchResult per branch,
// ordered by Seq, and the nested scopes the caller must cancel at the
// EventJoined's Seq. A round that closes complete has nothing pending.
func (r *caseRun) closeBranches(scope *Scope, policy JoinPolicy) ([]BranchResult, []*Scope, error) {
	// branchOf maps each scope open inside scope to the direct child standing
	// for its branch. r.scopes lists a parent before its children.
	branchOf := make(map[uuid.UUID]uuid.UUID)
	var nested []*Scope
	for _, s := range r.scopes {
		if s.Status != ScopeOpen || s.ParentID == nil {
			continue
		}
		if *s.ParentID == scope.ID {
			branchOf[s.ID] = s.ID
		} else if child, ok := branchOf[*s.ParentID]; ok {
			branchOf[s.ID] = child
		} else {
			continue
		}
		nested = append(nested, s)
	}

	var pending []Token
	for _, t := range r.tokens {
		_, inNested := branchOf[t.ScopeID]
		if (t.ScopeID == scope.ID && t.Status == TokenActive) || (inNested && t.Status.Live()) {
			pending = append(pending, *t)
		}
	}
	sortTokens(pending)

	var branches []BranchResult
	if len(pending) > 0 {
		detail, err := cancelDetail("join_policy:" + string(policy))
		if err != nil {
			return nil, nil, err
		}
		reported := make(map[uuid.UUID]bool)
		for i := range pending {
			t := r.token(pending[i].ID)
			child, inNested := branchOf[t.ScopeID]
			seq := r.emit(&Event{
				Kind: EventTokenCancelled, TokenID: t.ID, ScopeID: t.ScopeID,
				FromState: t.Node, ToState: t.Node, Detail: detail,
			})
			r.end(t, TokenCancelled, seq)
			if inNested && !reported[child] {
				reported[child] = true
				branches = append(branches, BranchResult{Token: t.ID, Outcome: OutcomeUnanswered, Seq: seq})
			}
		}
	}
	for _, t := range r.tokens {
		if t.ScopeID != scope.ID {
			continue
		}
		switch t.Status {
		case TokenWaiting:
			branches = append(branches, BranchResult{Token: t.ID, Outcome: t.Outcome, Seq: t.EnteredSeq})
		case TokenCancelled:
			branches = append(branches, BranchResult{Token: t.ID, Outcome: OutcomeUnanswered, Seq: t.EndedSeq})
		case TokenActive, TokenConsumed:
			// Nothing is active after the cancellations above, and a token a
			// nested split consumed is continued by another token or scope.
		}
	}
	sort.Slice(branches, func(i, j int) bool { return branches[i].Seq < branches[j].Seq })
	return branches, nested, nil
}

// validateJoinPolicies reports the violations of the policy rules through
// addf: a policy only on a join, and never on one an inclusive split names (it
// may route straight to its join, so that branch would have no decision); an
// outcome on exactly the transitions that enter a join with a policy, each
// leaving a task node; a required comment on a rejection under JoinAllApprove
// and JoinAnyApprove, so the reason is always recorded; a JoinCollect join
// reaching a task node, where a person decides; and a verdict join reaching a
// decision node, so a rejection can never advance by accident.
func validateJoinPolicies(d Definition, nodeByID map[string]Node, addf func(string, ...any)) {
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.JoinPolicy == "" {
			continue
		}
		if n.Kind != NodeJoin {
			addf("node %q: only a join may declare a join policy", n.ID)
			continue
		}
		if !n.JoinPolicy.Valid() {
			addf("join node %q: invalid join policy %q", n.ID, n.JoinPolicy)
		}
		for _, tr := range d.TransitionsFrom(n.ID) {
			to, ok := nodeByID[tr.To]
			if !ok {
				continue
			}
			switch n.JoinPolicy {
			case JoinCollect:
				if to.Kind.IsAutomatic() || to.Terminal {
					addf("join node %q: under %s its outgoing transition must reach a task node, got %q", n.ID, n.JoinPolicy, to.ID)
				}
			case JoinAllApprove, JoinAnyApprove:
				if to.Kind != NodeDecision {
					addf("join node %q: under %s its outgoing transition must reach a decision node, got %q", n.ID, n.JoinPolicy, to.ID)
				}
			}
		}
	}
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.Kind != NodeInclusive {
			continue
		}
		if j, ok := nodeByID[n.Join]; ok && j.Kind == NodeJoin && j.JoinPolicy != "" {
			addf("join node %q: a join policy needs a parallel split, but inclusive split %q names it", j.ID, n.ID)
		}
	}

	for i, tr := range d.Transitions {
		to, toOK := nodeByID[tr.To]
		if !toOK {
			continue
		}
		if to.Kind != NodeJoin || to.JoinPolicy == "" {
			if tr.Outcome != "" {
				addf("transition[%d]: must not declare an outcome: it does not enter a join with a policy", i)
			}
			continue
		}
		if from, ok := nodeByID[tr.From]; ok && from.Kind.IsAutomatic() {
			addf("transition[%d]: must leave a task node: it enters join %q, which has a policy, from %s %q", i, to.ID, from.Kind.label(), from.ID)
		}
		switch tr.Outcome {
		case OutcomeApproved:
		case OutcomeRejected:
			if to.JoinPolicy != JoinCollect && tr.Comment.Canonical() != CommentRequired {
				addf("transition[%d]: a rejected outcome under %s must require a comment", i, to.JoinPolicy)
			}
		case "":
			addf("transition[%d]: must declare an outcome: it enters join %q, which has a policy", i, to.ID)
		default:
			addf("transition[%d]: invalid outcome %q", i, tr.Outcome)
		}
	}
}
