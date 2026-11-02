package workflow

import (
	"testing"

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
