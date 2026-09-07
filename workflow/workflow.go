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
}

// Transition is a named move from one node to another, optionally gated by
// a registered guard.
type Transition struct {
	From   string
	To     string
	Action string
	// Guard names a GuardFunc registered on the Engine. Empty means the
	// transition is always permitted.
	Guard string
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

	for i, tr := range d.Transitions {
		if tr.Action == "" {
			addf("transition[%d]: empty action", i)
		}

		fromNode, fromOK := nodeByID[tr.From]
		_, toOK := nodeByID[tr.To]
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
		}

		key := tr.From + "\x00" + tr.Action
		if seenTransitionKeys[key] {
			addf("ambiguous transition: duplicate (from=%q, action=%q)", tr.From, tr.Action)
		}
		seenTransitionKeys[key] = true
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
	ID         uuid.UUID
	CaseID     uuid.UUID
	Seq        int64
	Kind       EventKind
	FromState  string
	ToState    string
	Action     string
	ActorID    string
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
)

// GuardFunc evaluates whether a transition may be taken for the given case.
// An error aborts the caller's operation rather than being treated as false.
type GuardFunc func(ctx context.Context, c Case) (bool, error)

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
