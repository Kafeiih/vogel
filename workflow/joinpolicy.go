package workflow

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

// validateJoinPolicies reports the violations of the policy rules through
// addf: a policy only on a join, and never on one an inclusive split names (it
// may route straight to its join, so that branch would have no decision); an
// outcome on exactly the transitions that enter a join with a policy, each
// leaving a task node; and a required comment on a rejection under
// JoinAllApprove and JoinAnyApprove, so the reason is always recorded.
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
