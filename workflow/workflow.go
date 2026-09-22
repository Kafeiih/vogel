// Package workflow is a generic workflow/BPM engine: it owns case state,
// transitions, assignment, and history for any process a consumer chooses
// to model as a Definition.
//
// The defining constraint is that the engine stores ZERO domain data. A
// Case carries only a reference — Domain and ExternalID, e.g.
// ("compras", "solicitud-123") — never the purchase order, the teacher
// record, or any other business payload. There is no form builder, no
// key/value "tracked data" table, and no JSON blob of user fields attached
// to a case. A consumer that needs to show "what is this case about" looks
// the referenced object up in its own domain store using (Domain,
// ExternalID); this package has no opinion on, and no access to, that data.
//
// Assignment is by organizational position and unit (see Eligibility), never
// by a person ID baked into the Definition. Resolving "who currently holds
// that position" — including deputy/subrogación handling — is deliberately
// left to a separate org-context service the caller owns: EligibilityFor
// returns the Eligibility for a case's current node, and the caller resolves
// it to actual people using whatever authority source it already has. This
// package never needs to know that resolution happened, or how.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

// Status represents the lifecycle state of a Case.
type Status string

const (
	StatusOpen   Status = "open"
	StatusClosed Status = "closed"
)

// Valid reports whether s is one of the known Status values.
func (s Status) Valid() bool {
	switch s {
	case StatusOpen, StatusClosed:
		return true
	default:
		return false
	}
}

// EventKind identifies the kind of change an Event records.
type EventKind string

const (
	EventOpened     EventKind = "opened"
	EventMoved      EventKind = "moved"
	EventAssigned   EventKind = "assigned"
	EventUnassigned EventKind = "unassigned"
	EventClosed     EventKind = "closed"
)

// Valid reports whether k is one of the known EventKind values.
func (k EventKind) Valid() bool {
	switch k {
	case EventOpened, EventMoved, EventAssigned, EventUnassigned, EventClosed:
		return true
	default:
		return false
	}
}

// CommentPolicy controls whether a comment is required, optional, or
// disallowed when a Transition is taken. It is process history, not domain
// data: the engine stores the comment text on the Event it appends, but
// never interprets it.
type CommentPolicy string

const (
	// CommentNone is the default: MoveInput.Comment must be empty (after
	// trimming) or Move fails with ErrCommentNotAllowed. Both the zero
	// value "" and the explicit literal "none" mean CommentNone, so a
	// Definition can spell out "no comment" without relying on the zero
	// value — see CommentPolicy.Canonical for how the alias is collapsed
	// back to this constant before any comparison.
	CommentNone CommentPolicy = ""
	// CommentOptional allows, but does not require, a comment.
	CommentOptional CommentPolicy = "optional"
	// CommentRequired fails Move with ErrCommentRequired when
	// MoveInput.Comment is empty after trimming.
	CommentRequired CommentPolicy = "required"
)

// commentPolicyNoneAlias is the sole spelling of the "none" alias literal.
// Valid and Canonical both reference this constant instead of repeating the
// literal, so the alias exists in exactly one place: a value comparing
// != CommentNone but == commentPolicyNoneAlias is unmistakably the alias,
// never a coincidentally similar typo.
const commentPolicyNoneAlias CommentPolicy = "none"

// Valid reports whether p is one of the known CommentPolicy values.
func (p CommentPolicy) Valid() bool {
	switch p {
	case CommentNone, commentPolicyNoneAlias, CommentOptional, CommentRequired:
		return true
	default:
		return false
	}
}

// Canonical collapses the "none" alias into the CommentNone zero value, so
// every comparison site sees the same value regardless of which spelling a
// Definition used. Definition.Validate cannot normalize transitions in
// place for the caller — it has a value receiver, so any mutation would be
// discarded when it returns — so canonicalization happens here instead, at
// every comparison point (currently just checkComment). Comparing a raw,
// non-canonicalized CommentPolicy against the CommentNone constant with ==
// silently disagrees for an alias-spelled value; call Canonical() first.
func (p CommentPolicy) Canonical() CommentPolicy {
	if p == commentPolicyNoneAlias {
		return CommentNone
	}
	return p
}

// checkComment validates comment (already trimmed) against policy. It is
// called from Engine.Move before any state change, so a rejected comment
// never leaves the case or its history mutated.
func checkComment(policy CommentPolicy, comment string) error {
	policy = policy.Canonical()
	switch {
	case policy == CommentRequired && comment == "":
		return ErrCommentRequired
	case policy == CommentNone && comment != "":
		return ErrCommentNotAllowed
	default:
		return nil
	}
}

// NodeKind distinguishes a Node that rests on human action (NodeTask, the
// default) from one that routes automatically on domain data evaluated by
// its outgoing transitions' guards (NodeDecision) — a BPMN-style exclusive
// gateway. A Case is never persisted resting on a NodeDecision: Engine.Move
// keeps routing through it, within the same call, until it reaches a
// NodeTask or terminal node.
type NodeKind string

const (
	// NodeTask is the default: a node a Case can rest on between human
	// actions, exactly like every Node before decision nodes existed. Both
	// the zero value "" and the explicit literal "task" mean NodeTask, so a
	// Definition can spell out "this is an ordinary node" without relying on
	// the zero value.
	NodeTask NodeKind = ""
	// NodeDecision marks a decision node: see the NodeKind doc comment.
	NodeDecision NodeKind = "decision"
)

// nodeTaskAlias is the sole spelling of the NodeTask "task" alias literal,
// mirroring commentPolicyNoneAlias: it exists in exactly one place so a
// value comparing != NodeTask but == nodeTaskAlias is unmistakably the
// alias. Unlike CommentPolicy, NodeKind needs no Canonical(): the only
// non-default spelling is NodeDecision, which has exactly one spelling, so
// every "is this a decision node?" check (Kind == NodeDecision) already
// agrees regardless of which NodeTask alias a Definition used.
const nodeTaskAlias NodeKind = "task"

// Valid reports whether k is one of the known NodeKind values.
func (k NodeKind) Valid() bool {
	switch k {
	case NodeTask, nodeTaskAlias, NodeDecision:
		return true
	default:
		return false
	}
}

// Eligibility declares who may act on a node, by organizational position.
// An empty Unit means "the unit the case belongs to" — see ResolveUnit.
//
// Eligibility never names a person. Turning a position+unit pair into the
// people who currently hold it (including deputy/subrogación resolution) is
// the caller's org-context service's job, not this package's.
type Eligibility struct {
	Position string
	Unit     string
}

// ByPosition builds an Eligibility scoped to the case's own unit.
func ByPosition(position string) Eligibility {
	return Eligibility{Position: position}
}

// ByPositionInUnit builds an Eligibility pinned to an explicit unit,
// overriding the case's own unit.
func ByPositionInUnit(position, unit string) Eligibility {
	return Eligibility{Position: position, Unit: unit}
}

// IsZero reports whether e declares no position at all.
func (e Eligibility) IsZero() bool {
	return e.Position == ""
}

// ResolveUnit returns e.Unit when explicitly set, or caseUnit otherwise.
func (e Eligibility) ResolveUnit(caseUnit string) string {
	if e.Unit != "" {
		return e.Unit
	}
	return caseUnit
}

// Node is one state a Case can occupy within a Definition.
type Node struct {
	ID       string
	Start    bool
	Terminal bool
	Eligible Eligibility
	// Deadline is the duration after entering this node before it is
	// considered overdue. Zero means no deadline.
	Deadline time.Duration
	// Kind distinguishes an ordinary task node from a decision node. The
	// zero value is NodeTask. See the NodeKind doc comment.
	Kind NodeKind
}

// Transition is a named move from one node to another, optionally gated by
// a registered guard. When From is a decision node, a Transition is instead
// called a route: routes are evaluated in declaration order by
// Engine.Move, and Definition.Validate requires exactly one unguarded route
// (the default), declared last.
type Transition struct {
	From   string
	To     string
	Action string
	// Guard names a GuardFunc registered on the Engine. Empty means the
	// transition (or, from a decision node, the route) is always permitted —
	// for a decision node's routes, this is what marks the default.
	Guard string
	// Comment declares this transition's CommentPolicy: whether MoveInput.
	// Comment is required, optional, or disallowed when this transition is
	// taken. The zero value is CommentNone. Definition.Validate rejects an
	// unrecognized value, and rejects any non-CommentNone value on a route
	// leaving a decision node — a route is domain-data routing, not a human
	// action, so it has nothing to comment on.
	Comment CommentPolicy
}

// Definition describes one version of a workflow: its nodes, and the
// transitions permitted between them.
type Definition struct {
	Name        string
	Version     int
	Nodes       []Node
	Transitions []Transition
}

// Node returns the node with the given ID, if any.
func (d Definition) Node(id string) (Node, bool) {
	for _, n := range d.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// StartNode returns the definition's single start node, if any.
func (d Definition) StartNode() (Node, bool) {
	for _, n := range d.Nodes {
		if n.Start {
			return n, true
		}
	}
	return Node{}, false
}

// TransitionsFrom returns every transition originating at nodeID.
func (d Definition) TransitionsFrom(nodeID string) []Transition {
	out := make([]Transition, 0, len(d.Transitions))
	for _, tr := range d.Transitions {
		if tr.From == nodeID {
			out = append(out, tr)
		}
	}
	return out
}

// GuardNames returns the sorted, deduplicated set of guard names referenced
// by this definition's transitions.
func (d Definition) GuardNames() []string {
	seen := make(map[string]bool, len(d.Transitions))
	names := make([]string, 0, len(d.Transitions))
	for _, tr := range d.Transitions {
		if tr.Guard == "" || seen[tr.Guard] {
			continue
		}
		seen[tr.Guard] = true
		names = append(names, tr.Guard)
	}
	sort.Strings(names)
	return names
}

// Key returns the unique key this definition is registered under: "Name@Version".
func (d Definition) Key() string {
	return fmt.Sprintf("%s@%d", d.Name, d.Version)
}

// Validate checks d for structural problems and returns a single error
// listing every violation found, wrapping ErrInvalidDefinition. It returns
// nil when d is well-formed.
func (d Definition) Validate() error {
	var errs []string
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	if d.Name == "" {
		addf("name is required")
	}
	if d.Version < 1 {
		addf("version must be >= 1, got %d", d.Version)
	}
	if len(d.Nodes) == 0 {
		addf("at least one node is required")
	}

	seenNodeIDs := make(map[string]bool, len(d.Nodes))
	nodeByID := make(map[string]Node, len(d.Nodes))
	startNodes, terminalNodes := 0, 0

	for i, n := range d.Nodes {
		if n.ID == "" {
			addf("node[%d]: empty ID", i)
		} else {
			if seenNodeIDs[n.ID] {
				addf("duplicate node ID %q", n.ID)
			}
			seenNodeIDs[n.ID] = true
			nodeByID[n.ID] = n
		}
		if !n.Kind.Valid() {
			addf("node %q: invalid kind %q", n.ID, n.Kind)
		}
		if n.Start && n.Terminal {
			addf("node %q: cannot be both start and terminal", n.ID)
		}
		if n.Start {
			startNodes++
		}
		if n.Terminal {
			terminalNodes++
		}
		if n.Deadline < 0 {
			addf("node %q: deadline must not be negative", n.ID)
		}

		if n.Kind == NodeDecision {
			if n.Start {
				addf("decision node %q: must not be a start node", n.ID)
			}
			if n.Terminal {
				addf("decision node %q: must not be a terminal node", n.ID)
			}
			if !n.Eligible.IsZero() {
				addf("decision node %q: must not declare an eligibility", n.ID)
			}
			if n.Deadline != 0 {
				addf("decision node %q: must not declare a deadline", n.ID)
			}
		}
	}

	if startNodes != 1 {
		addf("exactly one start node is required, found %d", startNodes)
	}
	if terminalNodes == 0 {
		addf("at least one terminal node is required")
	}

	seenTransitionKeys := make(map[string]bool, len(d.Transitions))
	outgoing := make(map[string]int, len(d.Nodes))
	adjacency := make(map[string][]string, len(d.Nodes))
	outgoingByNode := make(map[string][]Transition, len(d.Nodes))
	decisionAdjacency := make(map[string][]string, len(d.Nodes))

	for i, tr := range d.Transitions {
		if tr.Action == "" {
			addf("transition[%d]: empty action", i)
		}
		if !tr.Comment.Valid() {
			addf("transition[%d]: invalid comment policy %q", i, tr.Comment)
		}

		fromNode, fromOK := nodeByID[tr.From]
		toNode, toOK := nodeByID[tr.To]
		if !fromOK {
			addf("transition[%d]: unknown from node %q", i, tr.From)
		}
		if !toOK {
			addf("transition[%d]: unknown to node %q", i, tr.To)
		}
		if fromOK && fromNode.Terminal {
			addf("transition[%d]: originates from terminal node %q", i, tr.From)
		}
		if fromOK {
			outgoing[tr.From]++
			adjacency[tr.From] = append(adjacency[tr.From], tr.To)
			outgoingByNode[tr.From] = append(outgoingByNode[tr.From], tr)
		}

		if fromOK && fromNode.Kind == NodeDecision && tr.Comment.Canonical() != CommentNone {
			addf("transition[%d]: route from decision node %q must not declare a comment policy", i, tr.From)
		}
		if fromOK && toOK && fromNode.Kind == NodeDecision && toNode.Kind == NodeDecision {
			decisionAdjacency[tr.From] = append(decisionAdjacency[tr.From], tr.To)
		}

		key := tr.From + "\x00" + tr.Action
		if seenTransitionKeys[key] {
			addf("ambiguous transition: duplicate (from=%q, action=%q)", tr.From, tr.Action)
		}
		seenTransitionKeys[key] = true
	}

	for _, n := range d.Nodes {
		if n.ID == "" || n.Kind != NodeDecision {
			continue
		}
		routes := outgoingByNode[n.ID]
		if len(routes) < 2 {
			addf("decision node %q: requires at least two outgoing routes, found %d", n.ID, len(routes))
			continue
		}

		unguardedCount, unguardedIdx := 0, -1
		for i, r := range routes {
			if r.Guard == "" {
				unguardedCount++
				unguardedIdx = i
			}
		}
		switch {
		case unguardedCount == 0:
			addf("decision node %q: requires exactly one unguarded default route, found none", n.ID)
		case unguardedCount > 1:
			addf("decision node %q: requires exactly one unguarded default route, found %d", n.ID, unguardedCount)
		case unguardedIdx != len(routes)-1:
			addf("decision node %q: the unguarded default route (action %q) must be declared last", n.ID, routes[unguardedIdx].Action)
		}
	}

	if len(decisionAdjacency) > 0 {
		decisionNodes := make([]string, 0, len(d.Nodes))
		for _, n := range d.Nodes {
			if n.ID != "" && n.Kind == NodeDecision {
				decisionNodes = append(decisionNodes, n.ID)
			}
		}
		if cycle := detectDecisionCycle(decisionNodes, decisionAdjacency); cycle != nil {
			addf("decision-only cycle detected: %s", strings.Join(cycle, " -> "))
		}
	}

	for _, n := range d.Nodes {
		if n.ID == "" || n.Terminal {
			continue
		}
		if outgoing[n.ID] == 0 {
			addf("node %q: non-terminal node has no outgoing transition (dead end)", n.ID)
		}
	}

	if startNodes == 1 {
		start, _ := d.StartNode()
		reachable := map[string]bool{start.ID: true}
		queue := []string{start.ID}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, next := range adjacency[cur] {
				if !reachable[next] {
					reachable[next] = true
					queue = append(queue, next)
				}
			}
		}
		for _, n := range d.Nodes {
			if n.ID == "" {
				continue
			}
			if !reachable[n.ID] {
				addf("node %q: unreachable from start node", n.ID)
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w:\n  %s", ErrInvalidDefinition, strings.Join(errs, "\n  "))
}

// detectDecisionCycle returns the node IDs forming one cycle within adj — a
// directed graph already restricted to decision-kind nodes and the edges
// between them — or nil if adj is acyclic. nodes fixes DFS traversal order,
// so the result (and therefore Validate's error message) is deterministic
// across calls on the same Definition.
func detectDecisionCycle(nodes []string, adj map[string][]string) []string {
	const (
		white = iota
		gray
		black
	)
	color := make(map[string]int, len(nodes))
	var path []string
	var cycle []string

	var visit func(string) bool
	visit = func(n string) bool {
		color[n] = gray
		path = append(path, n)
		for _, next := range adj[n] {
			switch color[next] {
			case white:
				if visit(next) {
					return true
				}
			case gray:
				start := 0
				for i, p := range path {
					if p == next {
						start = i
						break
					}
				}
				cycle = append(append([]string{}, path[start:]...), next)
				return true
			}
		}
		path = path[:len(path)-1]
		color[n] = black
		return false
	}

	for _, n := range nodes {
		if color[n] == white {
			if visit(n) {
				return cycle
			}
		}
	}
	return nil
}

// Case is a single running (or completed) instance of a Definition. It
// carries only a reference to the domain object it concerns — Domain and
// ExternalID — never the object's own data.
type Case struct {
	ID         uuid.UUID
	Definition string // definition name
	Version    int
	Domain     string // e.g. "compras"
	ExternalID string // e.g. "solicitud-123"
	Unit       string // organizational unit the case belongs to
	State      string // current node ID
	Status     Status
	AssignedTo string // person id; empty when unclaimed
	OpenedAt   time.Time
	ClosedAt   *time.Time
	DeadlineAt *time.Time
}

// Event is a single append-only history record for a Case.
type Event struct {
	ID        uuid.UUID
	CaseID    uuid.UUID
	Seq       int64
	Kind      EventKind
	FromState string
	ToState   string
	Action    string
	ActorID   string
	// Comment is the (already trimmed) comment or observation recorded
	// alongside this event, when the taken transition's CommentPolicy
	// allowed one. It is always empty on the automatic EventClosed event
	// Move appends when entering a terminal node — the comment belongs to
	// the EventMoved record for that same transition, not to its close
	// side effect, so it is never duplicated there.
	Comment    string
	OccurredAt time.Time
}

// Sentinel errors callers can branch on via errors.Is.
var (
	ErrDefinitionNotRegistered = errors.New("workflow: definition not registered")
	ErrGuardNotRegistered      = errors.New("workflow: guard not registered")
	ErrCaseNotFound            = errors.New("workflow: case not found")
	ErrCaseExists              = errors.New("workflow: case already exists for that external reference")
	ErrCaseClosed              = errors.New("workflow: case is closed")
	ErrInvalidTransition       = errors.New("workflow: no such transition from current state")
	ErrGuardRejected           = errors.New("workflow: guard rejected the transition")
	ErrNotAssigned             = errors.New("workflow: case is not assigned")
	ErrAlreadyAssigned         = errors.New("workflow: case is already assigned")
	ErrInvalidDefinition       = errors.New("workflow: invalid definition")
	ErrCommentRequired         = errors.New("workflow: comment is required for this transition")
	ErrCommentNotAllowed       = errors.New("workflow: comment is not allowed for this transition")
	ErrNoRoute                 = errors.New("workflow: no route matched at decision node")
	ErrTooManyDecisionHops     = errors.New("workflow: exceeded maximum decision node hops")
)

// GuardFunc evaluates whether a transition may be taken for the given case.
// An error aborts the caller's operation rather than being treated as false.
//
// db is the exact value the caller passed to Engine.Available or
// Engine.Move — never a different connection, and never nil unless the
// caller itself passed nil. A guard that reads domain data (e.g. "does this
// purchase have an item with subsidy SEP?") must see writes made earlier in
// the caller's own transaction — for example a row the caller inserted just
// before calling Move — so the engine hands the guard the same db rather
// than opening a separate connection or using its own pool.
//
// Because db is shared with the caller's own transaction, a guard MUST
// return (never swallow) any database error it encounters, and MUST fully
// close any pgx.Rows it opens — via Rows.Close, not merely draining Next to
// false — before returning. A failed statement that is not reported leaves
// the transaction poisoned (PostgreSQL SQLSTATE 25P02, "current transaction
// is aborted"), and rows left open hold the underlying connection busy;
// either failure then breaks the engine's own writes later in the same call
// to Move.
type GuardFunc func(ctx context.Context, db pgxtx.DBTX, c Case) (bool, error)

// Repository defines persistence operations for cases and their event
// history. See workflow/postgres for the PostgreSQL-backed implementation.
type Repository interface {
	Create(ctx context.Context, db pgxtx.DBTX, c *Case) error
	GetByID(ctx context.Context, db pgxtx.DBTX, id uuid.UUID) (*Case, error)
	GetByExternalID(ctx context.Context, db pgxtx.DBTX, domain, externalID string) (*Case, error)
	Update(ctx context.Context, db pgxtx.DBTX, c *Case) error
	AppendEvent(ctx context.Context, db pgxtx.DBTX, e *Event) error
	ListEvents(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Event, error)
	ListByEligibility(ctx context.Context, db pgxtx.DBTX, f InboxFilter) ([]Case, error)
}

// InboxFilter selects open cases. States is the set of node IDs a caller
// considers itself eligible for — that mapping from Position/Unit to a set
// of eligible node IDs is resolved by the caller (see Engine.EligibilityFor
// and the package doc comment), not by this filter.
type InboxFilter struct {
	Domain     string
	States     []string
	AssignedTo string // empty = any
	Unassigned bool   // true = only unclaimed
	Overdue    bool   // true = only past DeadlineAt
	Limit      int
	Offset     int
}
