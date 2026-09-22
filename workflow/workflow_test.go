package workflow

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validDefinition returns a Definition that passes Validate(): a linear
// purchase-request flow with one start node, a review step, and two
// terminal outcomes.
func validDefinition() Definition {
	return Definition{
		Name:    "purchase",
		Version: 1,
		Nodes: []Node{
			{ID: "draft", Start: true, Eligible: ByPosition("buyer")},
			{ID: "review", Eligible: ByPositionInUnit("approver", "finance")},
			{ID: "approved", Terminal: true},
			{ID: "rejected", Terminal: true},
		},
		Transitions: []Transition{
			{From: "draft", To: "review", Action: "submit"},
			{From: "review", To: "approved", Action: "approve"},
			{From: "review", To: "rejected", Action: "reject"},
		},
	}
}

func TestDefinition_Validate_ValidDefinition_ReturnsNil(t *testing.T) {
	err := validDefinition().Validate()
	assert.NoError(t, err)
}

func TestDefinition_Validate_RejectsViolations(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(Definition) Definition
		wantSub string
	}{
		{
			name: "empty name",
			mutate: func(d Definition) Definition {
				d.Name = ""
				return d
			},
			wantSub: "name is required",
		},
		{
			name: "version less than 1",
			mutate: func(d Definition) Definition {
				d.Version = 0
				return d
			},
			wantSub: "version",
		},
		{
			name: "no nodes",
			mutate: func(d Definition) Definition {
				d.Nodes = nil
				return d
			},
			wantSub: "at least one node",
		},
		{
			name: "duplicate node ids",
			mutate: func(d Definition) Definition {
				d.Nodes = append(d.Nodes, Node{ID: "draft"})
				return d
			},
			wantSub: "duplicate node ID",
		},
		{
			name: "empty node id",
			mutate: func(d Definition) Definition {
				d.Nodes[1].ID = ""
				return d
			},
			wantSub: "empty ID",
		},
		{
			name: "node both start and terminal",
			mutate: func(d Definition) Definition {
				d.Nodes[0].Terminal = true
				return d
			},
			wantSub: "cannot be both start and terminal",
		},
		{
			name: "zero start nodes",
			mutate: func(d Definition) Definition {
				d.Nodes[0].Start = false
				return d
			},
			wantSub: "exactly one start node",
		},
		{
			name: "multiple start nodes",
			mutate: func(d Definition) Definition {
				d.Nodes[1].Start = true
				return d
			},
			wantSub: "exactly one start node",
		},
		{
			name: "no terminal node",
			mutate: func(d Definition) Definition {
				d.Nodes[2].Terminal = false
				d.Nodes[3].Terminal = false
				// avoid dead-end noise on the now-non-terminal nodes
				d.Transitions = append(d.Transitions,
					Transition{From: "approved", To: "draft", Action: "reopen"},
					Transition{From: "rejected", To: "draft", Action: "reopen"},
				)
				return d
			},
			wantSub: "at least one terminal node",
		},
		{
			name: "transition with empty action",
			mutate: func(d Definition) Definition {
				d.Transitions[0].Action = ""
				return d
			},
			wantSub: "empty action",
		},
		{
			name: "transition unknown from node",
			mutate: func(d Definition) Definition {
				d.Transitions[0].From = "ghost"
				return d
			},
			wantSub: `unknown from node "ghost"`,
		},
		{
			name: "transition unknown to node",
			mutate: func(d Definition) Definition {
				d.Transitions[0].To = "ghost"
				return d
			},
			wantSub: `unknown to node "ghost"`,
		},
		{
			name: "transition from terminal node",
			mutate: func(d Definition) Definition {
				d.Transitions = append(d.Transitions, Transition{From: "approved", To: "review", Action: "reopen"})
				return d
			},
			wantSub: "originates from terminal node",
		},
		{
			name: "non-terminal node with no outgoing transition",
			mutate: func(d Definition) Definition {
				d.Nodes = append(d.Nodes, Node{ID: "limbo"})
				d.Transitions = append(d.Transitions, Transition{From: "draft", To: "limbo", Action: "shelve"})
				return d
			},
			wantSub: `node "limbo": non-terminal node has no outgoing transition`,
		},
		{
			name: "duplicate (from, action) pair",
			mutate: func(d Definition) Definition {
				d.Transitions = append(d.Transitions, Transition{From: "draft", To: "rejected", Action: "submit"})
				return d
			},
			wantSub: "ambiguous transition",
		},
		{
			name: "unreachable node",
			mutate: func(d Definition) Definition {
				d.Nodes = append(d.Nodes, Node{ID: "orphan", Terminal: true})
				return d
			},
			wantSub: `node "orphan": unreachable from start node`,
		},
		{
			name: "negative deadline",
			mutate: func(d Definition) Definition {
				d.Nodes[1].Deadline = -1 * time.Second
				return d
			},
			wantSub: "deadline must not be negative",
		},
		{
			name: "invalid comment policy",
			mutate: func(d Definition) Definition {
				d.Transitions[1].Comment = CommentPolicy("bogus")
				return d
			},
			wantSub: "invalid comment policy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := tt.mutate(validDefinition())
			err := d.Validate()
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidDefinition), "error must wrap ErrInvalidDefinition")
			assert.Contains(t, err.Error(), tt.wantSub)
		})
	}
}

func TestDefinition_Validate_CollectsAllViolationsAtOnce(t *testing.T) {
	d := Definition{} // empty name, version 0, no nodes
	err := d.Validate()
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "name is required")
	assert.Contains(t, msg, "version")
	assert.Contains(t, msg, "at least one node")
}

func TestEligibility_ByPosition(t *testing.T) {
	e := ByPosition("approver")
	assert.Equal(t, "approver", e.Position)
	assert.Equal(t, "", e.Unit)
	assert.False(t, e.IsZero())
}

func TestEligibility_ByPositionInUnit(t *testing.T) {
	e := ByPositionInUnit("approver", "finance")
	assert.Equal(t, "approver", e.Position)
	assert.Equal(t, "finance", e.Unit)
}

func TestEligibility_IsZero(t *testing.T) {
	assert.True(t, Eligibility{}.IsZero())
	assert.False(t, ByPosition("x").IsZero())
}

func TestEligibility_ResolveUnit(t *testing.T) {
	tests := []struct {
		name     string
		elig     Eligibility
		caseUnit string
		want     string
	}{
		{"explicit unit wins", ByPositionInUnit("approver", "finance"), "sales", "finance"},
		{"falls back to case unit", ByPosition("approver"), "sales", "sales"},
		{"both empty", ByPosition("approver"), "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.elig.ResolveUnit(tt.caseUnit))
		})
	}
}

func TestStatus_Valid(t *testing.T) {
	assert.True(t, StatusOpen.Valid())
	assert.True(t, StatusClosed.Valid())
	assert.False(t, Status("bogus").Valid())
}

func TestEventKind_Valid(t *testing.T) {
	assert.True(t, EventOpened.Valid())
	assert.True(t, EventMoved.Valid())
	assert.True(t, EventAssigned.Valid())
	assert.True(t, EventUnassigned.Valid())
	assert.True(t, EventClosed.Valid())
	assert.False(t, EventKind("bogus").Valid())
}

func TestCommentPolicy_Valid(t *testing.T) {
	assert.True(t, CommentNone.Valid())
	assert.True(t, CommentPolicy("none").Valid())
	assert.True(t, CommentOptional.Valid())
	assert.True(t, CommentRequired.Valid())
	assert.False(t, CommentPolicy("bogus").Valid())
}

// TestCommentPolicy_Canonical proves the "none" alias collapses to the
// CommentNone constant in exactly one place (Canonical), so any code
// comparing a canonicalized policy against CommentNone with == agrees with
// isNone-style logic instead of silently disagreeing for an alias-spelled
// definition.
func TestCommentPolicy_Canonical(t *testing.T) {
	assert.Equal(t, CommentNone, CommentNone.Canonical())
	assert.Equal(t, CommentNone, CommentPolicy("none").Canonical())
	assert.Equal(t, CommentOptional, CommentOptional.Canonical())
	assert.Equal(t, CommentRequired, CommentRequired.Canonical())

	assert.True(t, CommentPolicy("none").Canonical() == CommentNone,
		"an alias-spelled policy must compare equal to CommentNone once canonicalized")
}

func TestDefinition_TransitionsFrom(t *testing.T) {
	d := validDefinition()
	got := d.TransitionsFrom("review")
	require.Len(t, got, 2)
	actions := []string{got[0].Action, got[1].Action}
	assert.ElementsMatch(t, []string{"approve", "reject"}, actions)

	assert.Empty(t, d.TransitionsFrom("approved"))
}

func TestDefinition_GuardNames_SortedAndDeduped(t *testing.T) {
	d := validDefinition()
	d.Transitions[1].Guard = "under-budget"
	d.Transitions[2].Guard = "always-true"
	d.Transitions = append(d.Transitions, Transition{From: "draft", To: "review", Action: "resubmit", Guard: "under-budget"})

	got := d.GuardNames()
	assert.Equal(t, []string{"always-true", "under-budget"}, got)
}

func TestDefinition_GuardNames_EmptyWhenNoGuards(t *testing.T) {
	d := validDefinition()
	assert.Empty(t, d.GuardNames())
}

func TestDefinition_Key(t *testing.T) {
	d := Definition{Name: "purchase", Version: 3}
	assert.Equal(t, "purchase@3", d.Key())
}

func TestDefinition_Node(t *testing.T) {
	d := validDefinition()

	n, ok := d.Node("review")
	require.True(t, ok)
	assert.Equal(t, "review", n.ID)

	_, ok = d.Node("ghost")
	assert.False(t, ok)
}

func TestNodeKind_Valid(t *testing.T) {
	assert.True(t, NodeTask.Valid())
	assert.True(t, NodeKind("task").Valid())
	assert.True(t, NodeDecision.Valid())
	assert.False(t, NodeKind("bogus").Valid())
}

// decisionDefinition returns a Definition exercising a D3 decision node:
// after "review", the decision node "route" picks "sep" when its guard
// matches, otherwise falls through to the default route "budget" — declared
// last, as Validate requires.
func decisionDefinition() Definition {
	return Definition{
		Name:    "purchase",
		Version: 1,
		Nodes: []Node{
			{ID: "draft", Start: true, Eligible: ByPosition("buyer")},
			{ID: "review", Eligible: ByPositionInUnit("approver", "finance")},
			{ID: "route", Kind: NodeDecision},
			{ID: "sep", Eligible: ByPosition("sep-coordinator")},
			{ID: "budget", Eligible: ByPosition("budget-analyst")},
			{ID: "approved", Terminal: true},
		},
		Transitions: []Transition{
			{From: "draft", To: "review", Action: "submit"},
			{From: "review", To: "route", Action: "approve"},
			{From: "route", To: "sep", Action: "to-sep", Guard: "has-subsidy-sep"},
			{From: "route", To: "budget", Action: "to-budget"},
			{From: "sep", To: "approved", Action: "clear"},
			{From: "budget", To: "approved", Action: "clear"},
		},
	}
}

func TestDefinition_Validate_DecisionDefinition_ReturnsNil(t *testing.T) {
	assert.NoError(t, decisionDefinition().Validate())
}

func TestDefinition_Validate_RejectsDecisionNodeViolations(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(Definition) Definition
		wantSub string
	}{
		{
			name: "invalid node kind",
			mutate: func(d Definition) Definition {
				d.Nodes[2].Kind = NodeKind("bogus")
				return d
			},
			wantSub: `node "route": invalid kind "bogus"`,
		},
		{
			name: "decision node as start",
			mutate: func(d Definition) Definition {
				d.Nodes[0].Start = false
				d.Nodes[2].Start = true
				return d
			},
			wantSub: `decision node "route": must not be a start node`,
		},
		{
			name: "decision node as terminal",
			mutate: func(d Definition) Definition {
				d.Nodes[2].Terminal = true
				return d
			},
			wantSub: `decision node "route": must not be a terminal node`,
		},
		{
			name: "decision node with eligibility",
			mutate: func(d Definition) Definition {
				d.Nodes[2].Eligible = ByPosition("someone")
				return d
			},
			wantSub: `decision node "route": must not declare an eligibility`,
		},
		{
			name: "decision node with deadline",
			mutate: func(d Definition) Definition {
				d.Nodes[2].Deadline = time.Hour
				return d
			},
			wantSub: `decision node "route": must not declare a deadline`,
		},
		{
			name: "fewer than two outgoing routes",
			mutate: func(d Definition) Definition {
				d.Transitions = d.Transitions[:3] // drop "route" -> "budget"
				// "budget" would otherwise be an unreachable dead end.
				d.Transitions = append(d.Transitions, Transition{From: "sep", To: "budget", Action: "reroute"})
				return d
			},
			wantSub: `decision node "route": requires at least two outgoing routes, found 1`,
		},
		{
			name: "no unguarded default route",
			mutate: func(d Definition) Definition {
				d.Transitions[3].Guard = "under-budget"
				return d
			},
			wantSub: `decision node "route": requires exactly one unguarded default route, found none`,
		},
		{
			name: "more than one unguarded route",
			mutate: func(d Definition) Definition {
				d.Transitions[2].Guard = ""
				return d
			},
			wantSub: `decision node "route": requires exactly one unguarded default route, found 2`,
		},
		{
			name: "unguarded default route not declared last",
			mutate: func(d Definition) Definition {
				d.Transitions[2], d.Transitions[3] = d.Transitions[3], d.Transitions[2]
				return d
			},
			wantSub: `decision node "route": the unguarded default route (action "to-budget") must be declared last`,
		},
		{
			name: "route declares a comment policy",
			mutate: func(d Definition) Definition {
				d.Transitions[3].Comment = CommentOptional
				return d
			},
			wantSub: `route from decision node "route" must not declare a comment policy`,
		},
		{
			name: "decision-only cycle",
			mutate: func(d Definition) Definition {
				d.Nodes = append(d.Nodes, Node{ID: "route2", Kind: NodeDecision})
				// route -> route2 replaces route -> sep (kept guarded), and
				// route2 routes back to route, forming a decision-only cycle.
				d.Transitions[2] = Transition{From: "route", To: "route2", Action: "to-route2", Guard: "has-subsidy-sep"}
				d.Transitions = append(d.Transitions,
					Transition{From: "route2", To: "route", Action: "loop-guarded", Guard: "has-subsidy-sep"},
					Transition{From: "route2", To: "budget", Action: "to-budget-2"},
				)
				return d
			},
			wantSub: "decision-only cycle detected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := tt.mutate(decisionDefinition())
			err := d.Validate()
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidDefinition), "error must wrap ErrInvalidDefinition")
			assert.Contains(t, err.Error(), tt.wantSub)
		})
	}
}

func TestDefinition_StartNode(t *testing.T) {
	d := validDefinition()

	n, ok := d.StartNode()
	require.True(t, ok)
	assert.Equal(t, "draft", n.ID)

	d.Nodes[0].Start = false
	_, ok = d.StartNode()
	assert.False(t, ok)
}
