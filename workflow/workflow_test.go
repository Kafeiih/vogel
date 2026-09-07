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

func TestDefinition_StartNode(t *testing.T) {
	d := validDefinition()

	n, ok := d.StartNode()
	require.True(t, ok)
	assert.Equal(t, "draft", n.ID)

	d.Nodes[0].Start = false
	_, ok = d.StartNode()
	assert.False(t, ok)
}
