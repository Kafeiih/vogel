package workflow

import (
	"fmt"
	"strings"
)

// block is the region a split bounds: the split, its join, and one branch per
// outgoing transition of the split. A token emitted by the split stays inside
// its branch until it arrives at the join, which is what lets the join count
// arrivals against Scope.Expected without dead-path elimination.
type block struct {
	split    Node
	join     string
	branches []branch
	// interior is the union of every branch's nodes.
	interior map[string]bool
}

// branch is the set of nodes reachable from one outgoing transition of a
// split without passing its join. Return transitions are not followed: they
// stay inside one branch by rule (see validateBlocks).
type branch struct {
	action string
	nodes  map[string]bool
	// order holds nodes in breadth-first order, so messages are
	// deterministic.
	order []string
	// reentersSplit reports a path from the branch back into its own split.
	reentersSplit bool
}

// contains reports whether id is the split, the join or an interior node of
// b.
func (b *block) contains(id string) bool {
	return id == b.split.ID || id == b.join || b.interior[id]
}

// members returns every node of b: the split, the interior in branch order,
// then the join.
func (b *block) members() []string {
	out := []string{b.split.ID}
	for _, br := range b.branches {
		out = append(out, br.order...)
	}
	return append(out, b.join)
}

// buildBlock computes the branches of split s from the outgoing transitions
// of every node.
func buildBlock(s Node, outgoing map[string][]Transition) *block {
	b := &block{split: s, join: s.Join, interior: make(map[string]bool)}
	for _, tr := range outgoing[s.ID] {
		br := branch{action: tr.Action, nodes: make(map[string]bool)}
		queue := []string{tr.To}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if id == s.Join || br.nodes[id] {
				continue
			}
			if id == s.ID {
				br.reentersSplit = true
				continue
			}
			br.nodes[id] = true
			br.order = append(br.order, id)
			for _, next := range outgoing[id] {
				if !next.Return {
					queue = append(queue, next.To)
				}
			}
		}
		for id := range br.nodes {
			b.interior[id] = true
		}
		b.branches = append(b.branches, br)
	}
	return b
}

// validateBlocks reports the block-structure violations of d through addf:
// every split names an existing join and every join is named by exactly one
// split; every path from a split's outgoing transition reaches its own join
// without a terminal node, without re-entering the split and without being
// entered from outside the block; sibling branches share no node; blocks
// either nest inside one branch of another block or are disjoint; and a
// return transition stays inside one branch of one block, or inside the root
// scope.
func validateBlocks(d Definition, nodeByID map[string]Node, outgoing map[string][]Transition, addf func(string, ...any)) {
	namedBy := make(map[string][]string)
	var blocks []*block
	for _, n := range d.Nodes {
		if n.ID == "" || !n.Kind.IsSplit() {
			continue
		}
		label := n.Kind.label()
		j, ok := nodeByID[n.Join]
		switch {
		case n.Join == "":
			addf("%s %q: must name its join (Node.Join)", label, n.ID)
		case !ok:
			addf("%s %q: names unknown join %q", label, n.ID, n.Join)
		case j.Kind != NodeJoin:
			addf("%s %q: names %q, which is not a join node", label, n.ID, n.Join)
		default:
			namedBy[n.Join] = append(namedBy[n.Join], n.ID)
			blocks = append(blocks, buildBlock(n, outgoing))
		}
	}
	for _, n := range d.Nodes {
		if n.ID == "" || n.Kind != NodeJoin {
			continue
		}
		switch splits := namedBy[n.ID]; len(splits) {
		case 0:
			addf("join node %q: is not named by any split", n.ID)
		case 1:
		default:
			quoted := make([]string, len(splits))
			for i, s := range splits {
				quoted[i] = fmt.Sprintf("%q", s)
			}
			addf("join node %q: is named by %d splits (%s), exactly one is required", n.ID, len(splits), strings.Join(quoted, ", "))
		}
	}

	incoming := make(map[string][]string, len(d.Nodes))
	for _, tr := range d.Transitions {
		if !tr.Return {
			incoming[tr.To] = append(incoming[tr.To], tr.From)
		}
	}
	for _, b := range blocks {
		checkBlock(b, nodeByID, incoming, addf)
	}
	checkNesting(blocks, addf)

	for i, tr := range d.Transitions {
		if !tr.Return {
			continue
		}
		if _, ok := nodeByID[tr.From]; !ok {
			continue
		}
		if _, ok := nodeByID[tr.To]; !ok {
			continue
		}
		if innermostBranch(blocks, tr.From) != innermostBranch(blocks, tr.To) {
			addf("transition[%d]: return transition from %q to %q crosses a block boundary", i, tr.From, tr.To)
		}
	}
}

// checkBlock reports the violations inside one block.
func checkBlock(b *block, nodeByID map[string]Node, incoming map[string][]string, addf func(string, ...any)) {
	label := b.split.Kind.label()
	for i, br := range b.branches {
		if br.reentersSplit {
			addf("%s %q: branch %q re-enters its split", label, b.split.ID, br.action)
		}
		for _, id := range br.order {
			if nodeByID[id].Terminal {
				addf("%s %q: branch %q reaches terminal node %q before its join %q", label, b.split.ID, br.action, id, b.join)
			}
		}

		// Walk backwards from the join inside the branch: a node it never
		// reaches can loop forever without arriving.
		reaches := make(map[string]bool, len(br.nodes))
		queue := []string{b.join}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			for _, from := range incoming[id] {
				if br.nodes[from] && !reaches[from] {
					reaches[from] = true
					queue = append(queue, from)
				}
			}
		}
		for _, id := range br.order {
			if !reaches[id] && !nodeByID[id].Terminal {
				addf("%s %q: branch %q never reaches its join %q from node %q", label, b.split.ID, br.action, b.join, id)
				break
			}
		}

		for _, other := range b.branches[i+1:] {
			for _, id := range br.order {
				if other.nodes[id] {
					addf("%s %q: branches %q and %q share node %q; a branch must stay apart from its siblings until the join",
						label, b.split.ID, br.action, other.action, id)
					break
				}
			}
		}
	}

	reported := make(map[string]bool)
	report := func(id, from, format string) {
		if from == b.split.ID || b.interior[from] || reported[id+"\x00"+from] {
			return
		}
		reported[id+"\x00"+from] = true
		addf(format, label, b.split.ID, id, from)
	}
	for _, br := range b.branches {
		for _, id := range br.order {
			for _, from := range incoming[id] {
				report(id, from, "%s %q: node %q is entered from %q outside the block")
			}
		}
	}
	for _, from := range incoming[b.join] {
		report(b.join, from, "%s %q: its join %q is entered from %q outside the block")
	}
}

// checkNesting reports pairs of blocks that neither nest inside one branch of
// the other nor stay disjoint.
func checkNesting(blocks []*block, addf func(string, ...any)) {
	for i, outer := range blocks {
		for k, inner := range blocks {
			if i == k || !outer.interior[inner.split.ID] {
				continue
			}
			var home *branch
			for bi := range outer.branches {
				if outer.branches[bi].nodes[inner.split.ID] {
					home = &outer.branches[bi]
					break
				}
			}
			for _, id := range inner.members() {
				if !home.nodes[id] {
					addf("%s %q: is not nested inside one branch of %s %q",
						inner.split.Kind.label(), inner.split.ID, outer.split.Kind.label(), outer.split.ID)
					break
				}
			}
		}
	}
	for i, a := range blocks {
		for _, b := range blocks[i+1:] {
			if a.interior[b.split.ID] || b.interior[a.split.ID] {
				continue
			}
			for _, id := range a.members() {
				if b.contains(id) {
					addf("blocks of %s %q and %s %q overlap without nesting",
						a.split.Kind.label(), a.split.ID, b.split.Kind.label(), b.split.ID)
					break
				}
			}
		}
	}
}

// branchKey identifies one branch of one block; the zero value is the root
// scope.
type branchKey struct {
	split string
	index int
}

// innermostBranch returns the smallest branch containing id, or the zero
// branchKey when id lies in the root scope. A split and its join belong to the
// branch around their block, not to one of its own branches.
func innermostBranch(blocks []*block, id string) branchKey {
	best, size := branchKey{}, -1
	for _, b := range blocks {
		for i, br := range b.branches {
			if br.nodes[id] && (size < 0 || len(br.nodes) < size) {
				best, size = branchKey{split: b.split.ID, index: i + 1}, len(br.nodes)
			}
		}
	}
	return best
}
