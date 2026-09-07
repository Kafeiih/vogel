package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

// Config configures optional Engine behaviour.
type Config struct {
	// Now returns the current time. Nil uses time.Now — inject a fixed clock
	// in tests.
	Now func() time.Time
}

// Engine runs a set of registered Definitions against a Repository: it
// opens cases, evaluates and applies transitions, tracks assignment, and
// records history.
type Engine struct {
	repo   Repository
	logger *slog.Logger
	now    func() time.Time

	mu          sync.RWMutex
	definitions map[string]Definition // by Key(): "Name@Version"
	latest      map[string]Definition // by bare Name -> highest registered Version
	guards      map[string]GuardFunc
}

// Option configures optional Engine behaviour at construction time.
type Option func(*Engine)

// New creates an Engine backed by repo. logger is required by the surrounding
// convention (libraries take a stdlib *slog.Logger); a nil logger falls back
// to slog.Default().
func New(repo Repository, cfg Config, logger *slog.Logger, opts ...Option) *Engine {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}

	e := &Engine{
		repo:        repo,
		logger:      logger,
		now:         now,
		definitions: make(map[string]Definition),
		latest:      make(map[string]Definition),
		guards:      make(map[string]GuardFunc),
	}

	for _, opt := range opts {
		opt(e)
	}

	return e
}

// WithDefinitions registers defs at construction time. It is a convenience
// over calling Register after New; a definition that fails validation is
// logged and skipped rather than aborting construction, since Option has no
// error return.
func WithDefinitions(defs ...Definition) Option {
	return func(e *Engine) {
		for _, d := range defs {
			if err := e.Register(d); err != nil {
				e.logger.Error("workflow: failed to register definition via WithDefinitions",
					"definition", d.Name, "version", d.Version, "error", err)
			}
		}
	}
}

// Register validates d and stores it under d.Key(). It also registers d
// under its bare Name, pointing at the highest version registered so far, so
// callers can open a case without pinning a version.
func (e *Engine) Register(d Definition) error {
	if err := d.Validate(); err != nil {
		return fmt.Errorf("workflow: register definition %q: %w", d.Key(), err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.definitions[d.Key()] = d
	if cur, ok := e.latest[d.Name]; !ok || d.Version > cur.Version {
		e.latest[d.Name] = d
	}
	return nil
}

// RegisterGuard registers fn under name. Overwriting an existing name is
// allowed but logged at Warn.
func (e *Engine) RegisterGuard(name string, fn GuardFunc) error {
	if name == "" {
		return errors.New("workflow: register guard: name is required")
	}
	if fn == nil {
		return errors.New("workflow: register guard: fn is required")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.guards[name]; exists {
		e.logger.Warn("workflow: overwriting registered guard", "guard", name)
	}
	e.guards[name] = fn
	return nil
}

// Definitions returns every registered definition, sorted by Name then
// Version, for introspection.
func (e *Engine) Definitions() []Definition {
	e.mu.RLock()
	defer e.mu.RUnlock()

	defs := make([]Definition, 0, len(e.definitions))
	for _, d := range e.definitions {
		defs = append(defs, d)
	}
	sort.Slice(defs, func(i, j int) bool {
		if defs[i].Name != defs[j].Name {
			return defs[i].Name < defs[j].Name
		}
		return defs[i].Version < defs[j].Version
	})
	return defs
}

// resolveDefinition looks up a registered definition by name and version.
// version == 0 resolves to the highest registered version for that name.
func (e *Engine) resolveDefinition(name string, version int) (Definition, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if version > 0 {
		d, ok := e.definitions[fmt.Sprintf("%s@%d", name, version)]
		if !ok {
			return Definition{}, fmt.Errorf("workflow: resolve definition %q version %d: %w", name, version, ErrDefinitionNotRegistered)
		}
		return d, nil
	}

	d, ok := e.latest[name]
	if !ok {
		return Definition{}, fmt.Errorf("workflow: resolve definition %q: %w", name, ErrDefinitionNotRegistered)
	}
	return d, nil
}

// lookupGuard returns the GuardFunc registered under name.
func (e *Engine) lookupGuard(name string) (GuardFunc, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	fn, ok := e.guards[name]
	if !ok {
		return nil, fmt.Errorf("workflow: guard %q: %w", name, ErrGuardNotRegistered)
	}
	return fn, nil
}

// OpenInput describes a new case to open.
type OpenInput struct {
	Definition string
	Version    int // 0 = latest registered version
	Domain     string
	ExternalID string
	Unit       string
	ActorID    string
}

// Open creates a case at the definition's start node and appends an
// EventOpened event as Seq 1. It is callable inside the caller's own
// transaction via db (see pgxtx.DBTX).
func (e *Engine) Open(ctx context.Context, db pgxtx.DBTX, in OpenInput) (*Case, error) {
	d, err := e.resolveDefinition(in.Definition, in.Version)
	if err != nil {
		return nil, err
	}

	start, ok := d.StartNode()
	if !ok {
		return nil, fmt.Errorf("workflow: open case: definition %q has no start node", d.Key())
	}

	now := e.now()
	c := &Case{
		ID:         uuid.New(),
		Definition: d.Name,
		Version:    d.Version,
		Domain:     in.Domain,
		ExternalID: in.ExternalID,
		Unit:       in.Unit,
		State:      start.ID,
		Status:     StatusOpen,
		OpenedAt:   now,
	}
	if start.Deadline > 0 {
		deadline := now.Add(start.Deadline)
		c.DeadlineAt = &deadline
	}

	if err := e.repo.Create(ctx, db, c); err != nil {
		return nil, fmt.Errorf("workflow: open case: %w", err)
	}

	evt := &Event{
		ID:         uuid.New(),
		CaseID:     c.ID,
		Seq:        1,
		Kind:       EventOpened,
		ToState:    start.ID,
		ActorID:    in.ActorID,
		OccurredAt: now,
	}
	if err := e.repo.AppendEvent(ctx, db, evt); err != nil {
		return nil, fmt.Errorf("workflow: open case: append event: %w", err)
	}

	return c, nil
}

// Available returns the transitions leaving the case's current state whose
// guard (if any) currently evaluates true. A guard returning an error aborts
// the whole call.
func (e *Engine) Available(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Transition, error) {
	c, err := e.repo.GetByID(ctx, db, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: available transitions: %w", err)
	}

	d, err := e.resolveDefinition(c.Definition, c.Version)
	if err != nil {
		return nil, err
	}

	candidates := d.TransitionsFrom(c.State)
	out := make([]Transition, 0, len(candidates))
	for _, tr := range candidates {
		if tr.Guard == "" {
			out = append(out, tr)
			continue
		}

		fn, err := e.lookupGuard(tr.Guard)
		if err != nil {
			return nil, err
		}
		ok, err := fn(ctx, *c)
		if err != nil {
			return nil, fmt.Errorf("workflow: available transitions: guard %q: %w", tr.Guard, err)
		}
		if ok {
			out = append(out, tr)
		}
	}
	return out, nil
}

// MoveInput describes a transition to apply to a case.
type MoveInput struct {
	CaseID  uuid.UUID
	Action  string
	ActorID string
}

// Move applies the transition matching (current state, Action) to the case.
// Moving into a terminal node closes the case. Assignment is always cleared
// on a move, since a new node means a new claim.
func (e *Engine) Move(ctx context.Context, db pgxtx.DBTX, in MoveInput) (*Case, error) {
	c, err := e.repo.GetByID(ctx, db, in.CaseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: move case: %w", err)
	}
	if c.Status == StatusClosed {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, ErrCaseClosed)
	}

	d, err := e.resolveDefinition(c.Definition, c.Version)
	if err != nil {
		return nil, err
	}

	transition, found := findTransition(d.TransitionsFrom(c.State), in.Action)
	if !found {
		return nil, fmt.Errorf("workflow: move case %s: action %q from state %q: %w", c.ID, in.Action, c.State, ErrInvalidTransition)
	}

	if transition.Guard != "" {
		fn, err := e.lookupGuard(transition.Guard)
		if err != nil {
			return nil, err
		}
		ok, err := fn(ctx, *c)
		if err != nil {
			return nil, fmt.Errorf("workflow: move case %s: guard %q: %w", c.ID, transition.Guard, err)
		}
		if !ok {
			return nil, fmt.Errorf("workflow: move case %s: guard %q: %w", c.ID, transition.Guard, ErrGuardRejected)
		}
	}

	destNode, ok := d.Node(transition.To)
	if !ok {
		return nil, fmt.Errorf("workflow: move case %s: destination node %q not found in definition %q", c.ID, transition.To, d.Key())
	}

	now := e.now()
	fromState := c.State
	c.State = destNode.ID
	c.AssignedTo = ""
	c.DeadlineAt = nil
	if destNode.Deadline > 0 {
		deadline := now.Add(destNode.Deadline)
		c.DeadlineAt = &deadline
	}

	terminated := destNode.Terminal
	if terminated {
		c.Status = StatusClosed
		c.ClosedAt = &now
		c.DeadlineAt = nil
	}

	if err := e.repo.Update(ctx, db, c); err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
	}

	if err := e.appendEvent(ctx, db, c.ID, EventMoved, fromState, destNode.ID, in.Action, in.ActorID, now); err != nil {
		return nil, fmt.Errorf("workflow: move case %s: append event: %w", c.ID, err)
	}

	if terminated {
		if err := e.appendEvent(ctx, db, c.ID, EventClosed, destNode.ID, destNode.ID, in.Action, in.ActorID, now); err != nil {
			return nil, fmt.Errorf("workflow: move case %s: append close event: %w", c.ID, err)
		}
	}

	return c, nil
}

// findTransition returns the transition in candidates matching action.
func findTransition(candidates []Transition, action string) (Transition, bool) {
	for _, tr := range candidates {
		if tr.Action == action {
			return tr, true
		}
	}
	return Transition{}, false
}

// Claim assigns the case to actorID. Re-claiming by the same actor is a
// no-op success; claiming a case already assigned to someone else fails.
func (e *Engine) Claim(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, actorID string) (*Case, error) {
	c, err := e.repo.GetByID(ctx, db, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: claim case: %w", err)
	}
	if c.Status == StatusClosed {
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, ErrCaseClosed)
	}
	if c.AssignedTo != "" {
		if c.AssignedTo == actorID {
			return c, nil
		}
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, ErrAlreadyAssigned)
	}

	c.AssignedTo = actorID
	if err := e.repo.Update(ctx, db, c); err != nil {
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, err)
	}

	if err := e.appendEvent(ctx, db, c.ID, EventAssigned, c.State, c.State, "", actorID, e.now()); err != nil {
		return nil, fmt.Errorf("workflow: claim case %s: append event: %w", c.ID, err)
	}

	return c, nil
}

// Release clears the case's assignment.
func (e *Engine) Release(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, actorID string) (*Case, error) {
	c, err := e.repo.GetByID(ctx, db, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: release case: %w", err)
	}
	if c.AssignedTo == "" {
		return nil, fmt.Errorf("workflow: release case %s: %w", c.ID, ErrNotAssigned)
	}

	c.AssignedTo = ""
	if err := e.repo.Update(ctx, db, c); err != nil {
		return nil, fmt.Errorf("workflow: release case %s: %w", c.ID, err)
	}

	if err := e.appendEvent(ctx, db, c.ID, EventUnassigned, c.State, c.State, "", actorID, e.now()); err != nil {
		return nil, fmt.Errorf("workflow: release case %s: append event: %w", c.ID, err)
	}

	return c, nil
}

// History returns every event recorded for caseID, ordered by Seq ascending.
func (e *Engine) History(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Event, error) {
	events, err := e.repo.ListEvents(ctx, db, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: history: %w", err)
	}

	sorted := make([]Event, len(events))
	copy(sorted, events)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Seq < sorted[j].Seq
	})
	return sorted, nil
}

// Inbox returns open cases matching f.
func (e *Engine) Inbox(ctx context.Context, db pgxtx.DBTX, f InboxFilter) ([]Case, error) {
	cases, err := e.repo.ListByEligibility(ctx, db, f)
	if err != nil {
		return nil, fmt.Errorf("workflow: inbox: %w", err)
	}
	return cases, nil
}

// EligibilityFor returns the current node's eligibility for caseID, with
// ResolveUnit already applied against the case's own unit. This is what a
// caller hands to its org-context service to resolve actual people.
func (e *Engine) EligibilityFor(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) (Eligibility, error) {
	c, err := e.repo.GetByID(ctx, db, caseID)
	if err != nil {
		return Eligibility{}, fmt.Errorf("workflow: eligibility: %w", err)
	}

	d, err := e.resolveDefinition(c.Definition, c.Version)
	if err != nil {
		return Eligibility{}, err
	}

	node, ok := d.Node(c.State)
	if !ok {
		return Eligibility{}, fmt.Errorf("workflow: eligibility: node %q not found in definition %q", c.State, d.Key())
	}

	return Eligibility{
		Position: node.Eligible.Position,
		Unit:     node.Eligible.ResolveUnit(c.Unit),
	}, nil
}

// appendEvent computes the next Seq for caseID and appends an event.
func (e *Engine) appendEvent(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, kind EventKind, fromState, toState, action, actorID string, occurredAt time.Time) error {
	seq, err := e.nextSeq(ctx, db, caseID)
	if err != nil {
		return err
	}

	evt := &Event{
		ID:         uuid.New(),
		CaseID:     caseID,
		Seq:        seq,
		Kind:       kind,
		FromState:  fromState,
		ToState:    toState,
		Action:     action,
		ActorID:    actorID,
		OccurredAt: occurredAt,
	}
	return e.repo.AppendEvent(ctx, db, evt)
}

// nextSeq returns the next per-case Seq value for caseID, computed from its
// existing event history.
func (e *Engine) nextSeq(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) (int64, error) {
	events, err := e.repo.ListEvents(ctx, db, caseID)
	if err != nil {
		return 0, fmt.Errorf("workflow: compute next seq: %w", err)
	}
	return nextSeqFromEvents(events), nil
}

// nextSeqFromEvents returns one past the highest Seq found in events, or 1
// when events is empty. Kept as a small, independently unit-testable helper.
func nextSeqFromEvents(events []Event) int64 {
	var max int64
	// Indexed rather than ranged by value: Event is 144 bytes and only Seq is
	// read here, over a case history that grows without bound.
	for i := range events {
		if events[i].Seq > max {
			max = events[i].Seq
		}
	}
	return max + 1
}
