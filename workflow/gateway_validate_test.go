package workflow

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parallelDefinition returns a valid Definition with one AND block: "fork"
// splits "draft" into the "legal" and "finance" branches, which meet again at
// "merge" before "signoff" closes the case.
func parallelDefinition() Definition {
	return Definition{
		Name:    "contract",
		Version: 1,
		Nodes: []Node{
			{ID: "draft", Start: true, Eligible: ByPosition("buyer")},
			{ID: "fork", Kind: NodeParallel, Join: "merge"},
			{ID: "legal", Eligible: ByPosition("lawyer")},
			{ID: "finance", Eligible: ByPosition("analyst")},
			{ID: "merge", Kind: NodeJoin},
			{ID: "signoff", Eligible: ByPosition("director")},
			{ID: "approved", Terminal: true},
		},
		Transitions: []Transition{
			{From: "draft", To: "fork", Action: "submit"},
			{From: "fork", To: "legal", Action: "to-legal"},
			{From: "fork", To: "finance", Action: "to-finance"},
			{From: "legal", To: "merge", Action: "clear"},
			{From: "finance", To: "merge", Action: "clear"},
			{From: "merge", To: "signoff", Action: "merged"},
			{From: "signoff", To: "approved", Action: "approve"},
		},
	}
}

// inclusiveDefinition returns a valid Definition with one OR block: "pick"
// takes "legal" when the "needs-legal" guard passes, "finance" when
// "needs-finance" passes, and the default route straight to "merge" when
// neither does.
func inclusiveDefinition() Definition {
	return Definition{
		Name:    "contract-or",
		Version: 1,
		Nodes: []Node{
			{ID: "draft", Start: true},
			{ID: "pick", Kind: NodeInclusive, Join: "merge"},
			{ID: "legal"},
			{ID: "finance"},
			{ID: "merge", Kind: NodeJoin},
			{ID: "approved", Terminal: true},
		},
		Transitions: []Transition{
			{From: "draft", To: "pick", Action: "submit"},
			{From: "pick", To: "legal", Action: "to-legal", Guard: "needs-legal"},
			{From: "pick", To: "finance", Action: "to-finance", Guard: "needs-finance"},
			{From: "pick", To: "merge", Action: "skip"},
			{From: "legal", To: "merge", Action: "clear"},
			{From: "finance", To: "merge", Action: "clear"},
			{From: "merge", To: "approved", Action: "merged"},
		},
	}
}

// nestedDefinition returns a valid Definition whose "legal" branch holds an
// inner AND block ("fork2" .. "merge2") inside the outer one.
func nestedDefinition() Definition {
	return Definition{
		Name:    "contract-nested",
		Version: 1,
		Nodes: []Node{
			{ID: "draft", Start: true},
			{ID: "fork", Kind: NodeParallel, Join: "merge"},
			{ID: "fork2", Kind: NodeParallel, Join: "merge2"},
			{ID: "contracts"},
			{ID: "compliance"},
			{ID: "merge2", Kind: NodeJoin},
			{ID: "finance"},
			{ID: "merge", Kind: NodeJoin},
			{ID: "approved", Terminal: true},
		},
		Transitions: []Transition{
			{From: "draft", To: "fork", Action: "submit"},
			{From: "fork", To: "fork2", Action: "to-legal"},
			{From: "fork", To: "finance", Action: "to-finance"},
			{From: "fork2", To: "contracts", Action: "to-contracts"},
			{From: "fork2", To: "compliance", Action: "to-compliance"},
			{From: "contracts", To: "merge2", Action: "clear"},
			{From: "compliance", To: "merge2", Action: "clear"},
			{From: "merge2", To: "merge", Action: "legal-done"},
			{From: "finance", To: "merge", Action: "clear"},
			{From: "merge", To: "approved", Action: "merged"},
		},
	}
}

func TestValidate_GatewayDefinitionsAreValid(t *testing.T) {
	for name, d := range map[string]Definition{
		"parallel":  parallelDefinition(),
		"inclusive": inclusiveDefinition(),
		"nested":    nestedDefinition(),
	} {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, d.Validate())
		})
	}
}

// requireInvalid asserts that d fails Validate with ErrInvalidDefinition and
// that the error lists every one of wantSubs.
func requireInvalid(t *testing.T, d Definition, wantSubs ...string) {
	t.Helper()
	err := d.Validate()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidDefinition), "error must wrap ErrInvalidDefinition")
	for _, sub := range wantSubs {
		assert.Contains(t, err.Error(), sub)
	}
}

func TestValidate_ParallelSplitRequiresMatchingJoin(t *testing.T) {
	t.Run("no join named", func(t *testing.T) {
		d := parallelDefinition()
		d.Nodes[1].Join = ""
		requireInvalid(t, d, `parallel split "fork": must name its join`)
	})
	t.Run("unknown join", func(t *testing.T) {
		d := parallelDefinition()
		d.Nodes[1].Join = "nowhere"
		requireInvalid(t, d, `parallel split "fork": names unknown join "nowhere"`)
	})
	t.Run("join names a task node", func(t *testing.T) {
		d := parallelDefinition()
		d.Nodes[1].Join = "signoff"
		requireInvalid(t, d, `parallel split "fork": names "signoff", which is not a join node`)
	})
	t.Run("a task node names a join", func(t *testing.T) {
		d := parallelDefinition()
		d.Nodes[0].Join = "merge"
		requireInvalid(t, d, `node "draft": only a split may name a join`)
	})
}

func TestValidate_JoinNamedByExactlyOneSplit(t *testing.T) {
	t.Run("not named", func(t *testing.T) {
		d := parallelDefinition()
		d.Nodes = append(d.Nodes, Node{ID: "orphan", Kind: NodeJoin})
		d.Transitions = append(d.Transitions,
			Transition{From: "signoff", To: "orphan", Action: "orphan"},
			Transition{From: "orphan", To: "approved", Action: "out"},
		)
		requireInvalid(t, d, `join node "orphan": is not named by any split`)
	})
	t.Run("named twice", func(t *testing.T) {
		d := parallelDefinition()
		d.Nodes = append(d.Nodes, Node{ID: "fork-b", Kind: NodeParallel, Join: "merge"})
		d.Transitions = append(d.Transitions,
			Transition{From: "signoff", To: "fork-b", Action: "again"},
			Transition{From: "fork-b", To: "merge", Action: "a"},
			Transition{From: "fork-b", To: "merge", Action: "b"},
		)
		requireInvalid(t, d, `join node "merge": is named by 2 splits ("fork", "fork-b"), exactly one is required`)
	})
}

func TestValidate_BranchMustNotReachTerminal(t *testing.T) {
	d := parallelDefinition()
	d.Nodes = append(d.Nodes, Node{ID: "rejected", Terminal: true})
	d.Transitions = append(d.Transitions, Transition{From: "legal", To: "rejected", Action: "reject"})
	requireInvalid(t, d, `parallel split "fork": branch "to-legal" reaches terminal node "rejected" before its join "merge"`)
}

func TestValidate_RejectsImproperlyNestedBlocks(t *testing.T) {
	t.Run("branches share a node", func(t *testing.T) {
		d := parallelDefinition()
		d.Transitions = append(d.Transitions, Transition{From: "legal", To: "finance", Action: "hand-over"})
		requireInvalid(t, d, `parallel split "fork": branches "to-legal" and "to-finance" share node "finance"`)
	})
	t.Run("node entered from outside the block", func(t *testing.T) {
		d := parallelDefinition()
		d.Transitions = append(d.Transitions, Transition{From: "draft", To: "legal", Action: "shortcut"})
		requireInvalid(t, d, `parallel split "fork": node "legal" is entered from "draft" outside the block`)
	})
	t.Run("branch re-enters its split", func(t *testing.T) {
		d := parallelDefinition()
		d.Transitions = append(d.Transitions, Transition{From: "legal", To: "fork", Action: "again"})
		requireInvalid(t, d, `parallel split "fork": branch "to-legal" re-enters its split`)
	})
	t.Run("inner branch jumps to the outer join", func(t *testing.T) {
		d := nestedDefinition()
		d.Transitions = append(d.Transitions, Transition{From: "contracts", To: "merge", Action: "escape"})
		requireInvalid(t, d, `parallel split "fork2": is not nested inside one branch of parallel split "fork"`)
	})
	t.Run("branch never reaches its join", func(t *testing.T) {
		d := parallelDefinition()
		d.Nodes = append(d.Nodes, Node{ID: "loop"})
		d.Transitions[3] = Transition{From: "legal", To: "loop", Action: "clear"}
		d.Transitions = append(d.Transitions,
			Transition{From: "loop", To: "legal", Action: "back"},
		)
		requireInvalid(t, d, `parallel split "fork": branch "to-legal" never reaches its join "merge" from node "legal"`)
	})
}

func TestValidate_RejectsReturnAcrossBlock(t *testing.T) {
	t.Run("branch to root", func(t *testing.T) {
		d := parallelDefinition()
		d.Transitions = append(d.Transitions, Transition{From: "legal", To: "draft", Action: "back", Return: true, Comment: CommentRequired})
		requireInvalid(t, d, `return transition from "legal" to "draft" crosses a block boundary`)
	})
	t.Run("root to branch", func(t *testing.T) {
		d := parallelDefinition()
		d.Transitions = append(d.Transitions, Transition{From: "signoff", To: "legal", Action: "back", Return: true})
		requireInvalid(t, d, `return transition from "signoff" to "legal" crosses a block boundary`)
	})
	t.Run("between sibling branches", func(t *testing.T) {
		d := parallelDefinition()
		d.Transitions = append(d.Transitions, Transition{From: "finance", To: "legal", Action: "back", Return: true})
		requireInvalid(t, d, `return transition from "finance" to "legal" crosses a block boundary`)
	})
	t.Run("inside one branch is valid", func(t *testing.T) {
		d := parallelDefinition()
		d.Nodes = append(d.Nodes, Node{ID: "legal-review"})
		d.Transitions[3] = Transition{From: "legal", To: "legal-review", Action: "clear"}
		d.Transitions = append(d.Transitions,
			Transition{From: "legal-review", To: "merge", Action: "clear"},
			Transition{From: "legal-review", To: "legal", Action: "back", Return: true},
		)
		assert.NoError(t, d.Validate())
	})
	t.Run("inside the root scope is valid", func(t *testing.T) {
		d := parallelDefinition()
		d.Transitions = append(d.Transitions, Transition{From: "signoff", To: "draft", Action: "back", Return: true})
		assert.NoError(t, d.Validate())
	})
}

func TestValidate_InclusiveSplitNeedsDefaultLast(t *testing.T) {
	t.Run("no default", func(t *testing.T) {
		d := inclusiveDefinition()
		d.Transitions[3].Guard = "needs-nothing"
		requireInvalid(t, d, `inclusive split "pick": requires exactly one unguarded default route, found none`)
	})
	t.Run("default not last", func(t *testing.T) {
		d := inclusiveDefinition()
		d.Transitions[2], d.Transitions[3] = d.Transitions[3], d.Transitions[2]
		requireInvalid(t, d, `inclusive split "pick": the unguarded default route (action "skip") must be declared last`)
	})
	t.Run("two defaults", func(t *testing.T) {
		d := inclusiveDefinition()
		d.Transitions[2].Guard = ""
		requireInvalid(t, d, `inclusive split "pick": requires exactly one unguarded default route, found 2`)
	})
}

func TestValidate_ParallelSplitRejectsGuards(t *testing.T) {
	d := parallelDefinition()
	d.Transitions[1].Guard = "needs-legal"
	requireInvalid(t, d, `transition[1]: route from parallel split "fork" must not declare a guard`)
}

func TestValidate_SplitNeedsTwoRoutes(t *testing.T) {
	d := parallelDefinition()
	d.Transitions = append(d.Transitions[:2:2], d.Transitions[3:]...) // drop fork -> finance
	d.Transitions = append(d.Transitions, Transition{From: "draft", To: "finance", Action: "direct"})
	requireInvalid(t, d, `parallel split "fork": requires at least two outgoing routes, found 1`)
}

func TestValidate_GatewaysRejectEligibilityDeadlineEffects(t *testing.T) {
	for _, tt := range []struct {
		idx   int
		label string
	}{
		{1, `parallel split "fork"`},
		{4, `join node "merge"`},
	} {
		d := parallelDefinition()
		d.Nodes[tt.idx].Eligible = ByPosition("someone")
		d.Nodes[tt.idx].Deadline = time.Hour
		d.Nodes[tt.idx].Effects = []string{"notify"}
		requireInvalid(t, d,
			tt.label+": must not declare an eligibility",
			tt.label+": must not declare a deadline",
			tt.label+": must not declare effects",
		)
	}

	d := parallelDefinition()
	d.Nodes[1].Start, d.Nodes[0].Start = true, false
	d.Nodes[4].Terminal = true
	requireInvalid(t, d,
		`parallel split "fork": must not be a start node`,
		`join node "merge": must not be a terminal node`,
	)

	// Their transitions may declare effects.
	d = parallelDefinition()
	d.Transitions[1].Effects = []string{"notify"}
	d.Transitions[5].Effects = []string{"notify"}
	assert.NoError(t, d.Validate())
}

func TestValidate_RejectsCommentOnSplitRoute(t *testing.T) {
	d := parallelDefinition()
	d.Transitions[1].Comment = CommentOptional
	requireInvalid(t, d, `transition[1]: route from parallel split "fork" must not declare a comment policy`)
}

func TestValidate_RejectsReturnIntoOrOutOfGateway(t *testing.T) {
	d := parallelDefinition()
	d.Transitions = append(d.Transitions, Transition{From: "signoff", To: "merge", Action: "back", Return: true})
	requireInvalid(t, d, `return transition must target a task node, got join node "merge"`)
}

func TestValidate_JoinHasExactlyOneUnguardedOutgoing(t *testing.T) {
	t.Run("two outgoing", func(t *testing.T) {
		d := parallelDefinition()
		d.Transitions = append(d.Transitions, Transition{From: "merge", To: "approved", Action: "skip-signoff"})
		requireInvalid(t, d, `join node "merge": requires exactly one outgoing transition, found 2`)
	})
	t.Run("guarded", func(t *testing.T) {
		d := parallelDefinition()
		d.Transitions[5].Guard = "ok"
		requireInvalid(t, d, `join node "merge": its outgoing transition must not declare a guard`)
	})
}

func TestValidate_RejectsGatewayOnlyCycle(t *testing.T) {
	// "merge" leads to the decision "again", whose guarded route goes back to
	// "fork": fork -> merge -> again -> fork never rests on a task node.
	d := parallelDefinition()
	d.Nodes = append(d.Nodes, Node{ID: "again", Kind: NodeDecision})
	d.Transitions[5] = Transition{From: "merge", To: "again", Action: "merged"}
	d.Transitions = append(d.Transitions,
		Transition{From: "fork", To: "merge", Action: "skip"},
		Transition{From: "again", To: "fork", Action: "loop", Guard: "loop"},
		Transition{From: "again", To: "signoff", Action: "done"},
	)
	requireInvalid(t, d, "gateway-only cycle detected: fork -> merge -> again -> fork")
}
