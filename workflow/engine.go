package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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

	// effectSink receives the events Open and Move append; nil means no
	// definition may declare effects. Set only at construction.
	effectSink EffectSink
	// pendingDefs collects WithDefinitions arguments so New registers them
	// after every option ran, making WithEffectSink order-independent.
	pendingDefs []Definition
}

// EffectSink turns the declarative effects a Definition names (Node.Effects,
// Transition.Effects) into work that runs outside the request advancing the
// case — typically by enqueuing jobs in the same transaction. The engine
// itself never runs an effect and never enqueues anything: it only asks the
// sink, at Register time, whether each name is known, and hands it the
// events each Open or Move appended.
type EffectSink interface {
	// HasEffect reports whether name is an effect this sink can dispatch.
	HasEffect(name string) bool
	// CheckDB reports whether Dispatch could run on db. Open and Move call
	// it before writing anything, whenever the case's definition declares
	// at least one effect, so a db the sink cannot dispatch on (for example
	// a pool where a transaction is required) fails the call with nothing
	// persisted, instead of committing a transition whose effects are then
	// lost.
	CheckDB(db pgxtx.DBTX) error
	// Dispatch is called by Open and Move exactly once, after all their
	// persistence succeeded, with the definition the case runs on, the final
	// case, and exactly the events that call appended, in Seq order (Open:
	// the EventOpened; Move: the human-driven EventMoved, every automatic
	// decision-node EventMoved, then the automatic EventClosed if the case
	// closed). db is the exact value the caller passed to Open or Move, so
	// whatever Dispatch writes commits or rolls back with the caller's
	// transaction. A returned error fails Open or Move (wrapped); the caller
	// must then roll back. Like a GuardFunc, Dispatch runs inside that open
	// transaction and must not call the network.
	Dispatch(ctx context.Context, db pgxtx.DBTX, d Definition, c Case, events []Event) error
}

// Option configures optional Engine behaviour at construction time.
type Option func(*Engine)

// WithEffectSink sets the EffectSink that validates effect names at Register
// time and receives the events Open and Move append. Without one, Register
// rejects any definition that declares an effect, and nothing is dispatched.
// It may appear before or after WithDefinitions.
func WithEffectSink(s EffectSink) Option {
	return func(e *Engine) {
		e.effectSink = s
	}
}

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

	// Definitions register only after every option ran, so a WithEffectSink
	// listed after WithDefinitions still validates their effects.
	for _, d := range e.pendingDefs {
		if err := e.Register(d); err != nil {
			e.logger.Error("workflow: failed to register definition via WithDefinitions",
				"definition", d.Name, "version", d.Version, "error", err)
		}
	}
	e.pendingDefs = nil

	return e
}

// WithDefinitions registers defs at construction time, after every other
// option has been applied. It is a convenience over calling Register after
// New; a definition that fails validation (or names an unknown effect) is
// logged and skipped rather than aborting construction, since Option has no
// error return.
func WithDefinitions(defs ...Definition) Option {
	return func(e *Engine) {
		e.pendingDefs = append(e.pendingDefs, defs...)
	}
}

// Register validates d and stores it under d.Key(). It also registers d
// under its bare Name, pointing at the highest version registered so far, so
// callers can open a case without pinning a version. A definition naming an
// effect fails with ErrUnknownEffect when no EffectSink is set, or when the
// sink's HasEffect reports false for that name.
func (e *Engine) Register(d Definition) error {
	if err := d.Validate(); err != nil {
		return fmt.Errorf("workflow: register definition %q: %w", d.Key(), err)
	}
	if err := e.checkEffects(d); err != nil {
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

// checkEffects reports every effect name d declares that the Engine's
// EffectSink cannot dispatch (all of them when no sink is set).
func (e *Engine) checkEffects(d Definition) error {
	var unknown []string
	for _, name := range d.EffectNames() {
		if e.effectSink == nil || !e.effectSink.HasEffect(name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	if e.effectSink == nil {
		return fmt.Errorf("%w: no effect sink set for %s", ErrUnknownEffect, strings.Join(unknown, ", "))
	}
	return fmt.Errorf("%w: %s", ErrUnknownEffect, strings.Join(unknown, ", "))
}

// checkEffectDB asks the EffectSink, before any write, whether it could
// dispatch d's effects on db. Definitions without effects never need it.
func (e *Engine) checkEffectDB(d Definition, db pgxtx.DBTX) error {
	if e.effectSink == nil || len(d.EffectNames()) == 0 {
		return nil
	}
	return e.effectSink.CheckDB(db)
}

// dispatch hands the events one Open or Move appended to the EffectSink, if
// any. It is the engine's only contact with effects.
func (e *Engine) dispatch(ctx context.Context, db pgxtx.DBTX, d Definition, c Case, events []Event) error {
	if e.effectSink == nil {
		return nil
	}
	return e.effectSink.Dispatch(ctx, db, d, c, events)
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
// transaction via db (see pgxtx.DBTX). When an EffectSink is set, Open then
// calls its Dispatch once with that event; a Dispatch error fails Open and
// the caller must roll back its transaction, discarding the new case.
func (e *Engine) Open(ctx context.Context, db pgxtx.DBTX, in OpenInput) (*Case, error) {
	d, err := e.resolveDefinition(in.Definition, in.Version)
	if err != nil {
		return nil, err
	}

	start, ok := d.StartNode()
	if !ok {
		return nil, fmt.Errorf("workflow: open case: definition %q has no start node", d.Key())
	}
	if err := e.checkEffectDB(d, db); err != nil {
		return nil, fmt.Errorf("workflow: open case: %w", err)
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

	if err := e.dispatch(ctx, db, d, *c, []Event{*evt}); err != nil {
		return nil, fmt.Errorf("workflow: open case %s: dispatch effects: %w", c.ID, err)
	}

	return c, nil
}

// Available returns the transitions leaving the case's current state whose
// guard (if any) currently evaluates true. A return transition (Transition.
// Return) is filtered out unless the case previously occupied its To node
// (see occupiedStates) — checked before the guard, exactly like Engine.Move
// orders the same two checks. A guard returning an error aborts the whole
// call. The case's event history is read at most once, and only when at
// least one candidate transition is a return.
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

	var occupied map[string]bool
	if hasReturnTransition(candidates) {
		occupied, err = e.occupiedStates(ctx, db, caseID)
		if err != nil {
			return nil, fmt.Errorf("workflow: available transitions: %w", err)
		}
	}

	out := make([]Transition, 0, len(candidates))
	for _, tr := range candidates {
		if tr.Return && !occupied[tr.To] {
			continue
		}
		if tr.Guard == "" {
			out = append(out, tr)
			continue
		}

		fn, err := e.lookupGuard(tr.Guard)
		if err != nil {
			return nil, err
		}
		ok, err := fn(ctx, db, *c)
		if err != nil {
			return nil, fmt.Errorf("workflow: available transitions: guard %q: %w", tr.Guard, err)
		}
		if ok {
			out = append(out, tr)
		}
	}
	return out, nil
}

// hasReturnTransition reports whether any transition in candidates is a
// return transition. Available and Move both call this before reading the
// case's event history, so that read only ever happens when it can actually
// affect the result.
func hasReturnTransition(candidates []Transition) bool {
	for _, tr := range candidates {
		if tr.Return {
			return true
		}
	}
	return false
}

// occupiedStates returns every node ID the case identified by caseID has
// entered or left, derived entirely from its own event history: the node it
// was opened at (EventOpened.ToState), plus every EventMoved record's
// FromState and ToState — including decision nodes the case automatically
// routed through, since each decision-node hop appends its own EventMoved
// (see planMove). It is the single source of truth Transition.Return checks
// against — never the Definition's graph reachability — so a return is only
// ever offered or accepted for a node this specific case actually passed
// through. A decision node can appear in this set, but that can never make a
// return into one possible: Definition.Validate rejects any return
// transition whose To is a decision node, regardless of occupancy.
func (e *Engine) occupiedStates(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) (map[string]bool, error) {
	events, err := e.repo.ListEvents(ctx, db, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: resolve occupied states: %w", err)
	}

	occupied := make(map[string]bool, len(events))
	// Indexed rather than ranged by value, like nextSeqFromEvents: Event is
	// 144 bytes and only Kind/FromState/ToState are read here, over a case
	// history that grows without bound.
	for i := range events {
		switch events[i].Kind {
		case EventOpened:
			occupied[events[i].ToState] = true
		case EventMoved:
			occupied[events[i].FromState] = true
			occupied[events[i].ToState] = true
		default:
			// EventAssigned, EventUnassigned, and EventClosed never add a
			// node this set doesn't already have: EventClosed shares its
			// FromState/ToState with the terminal node's own EventMoved
			// (already counted), and Assigned/Unassigned carry the current
			// state unchanged.
		}
	}
	return occupied, nil
}

// maxDecisionHops bounds how many decision-node routing hops Move will
// follow in a single call before giving up. Definition.Validate rejects any
// cycle made up only of decision nodes, so a validated Definition can never
// come close to needing this many hops; the cap exists purely as a defense
// against a Definition that reached the engine without going through
// Validate (or a future bug in it) spinning Move forever.
const maxDecisionHops = 32

// decisionHop records one EventMoved worth of routing. Move builds the full
// chain of hops — the human-driven move and every automatic decision-node
// hop it leads to — in planMove, entirely in memory, before writing
// anything: only once the chain is known to end on a task or terminal node
// does Move persist the case and append one event per hop.
type decisionHop struct {
	fromState string
	toState   string
	action    string
	// comment is only ever non-empty on the first (human-driven) hop: an
	// automatic decision-node hop has nothing a human said to record.
	comment string
}

// MoveInput describes a transition to apply to a case.
type MoveInput struct {
	CaseID  uuid.UUID
	Action  string
	ActorID string
	// Comment is the step comment (on approval) or observation (on
	// rejection) attached to this move. It is trimmed with strings.
	// TrimSpace, then validated against the taken transition's
	// CommentPolicy before any state change: empty after trimming on a
	// CommentRequired transition fails with ErrCommentRequired, and
	// non-empty on a CommentNone transition fails with
	// ErrCommentNotAllowed. It is stored as-is (trimmed) on the resulting
	// EventMoved record.
	Comment string
}

// Move applies the transition matching (current state, Action) to the case,
// then — if that lands on a decision node — keeps routing automatically,
// within this same call and the same db transaction, until the case comes
// to rest on a task node or a terminal node. A case is never persisted
// resting on a decision node. Moving into a terminal node closes the case.
// Assignment is always cleared on a move, since a new node means a new
// claim.
//
// The human-driven transition is checked in a fixed order before anything
// is mutated: first its CommentPolicy (see checkComment), then — only if it
// is a return transition (Transition.Return) — whether the case previously
// occupied its To node (see occupiedStates), then its Guard, if any. A
// return whose target the case never occupied fails with
// ErrReturnNotVisited before the guard ever runs, exactly like a rejected
// comment fails before the guard runs; either failure leaves the case and
// its history completely untouched. A return transition that passes all
// three checks is applied exactly like any other move: Deadline and
// eligibility are recomputed for the node the case comes to rest on, and
// assignment is cleared, same as always — Move has no separate "undo"
// semantics for a return, it is just an ordinary transition whose
// destination happens to be an earlier node in the case's own history.
//
// Each hop along the way — the initial human-driven move and every
// automatic decision-node hop after it — appends its own EventMoved, with
// Action set to that hop's transition (or route) label and ActorID set to
// the original MoveInput.ActorID throughout. Only the first hop ever
// carries in.Comment; every automatic hop records an empty Comment, since
// the comment belongs to the human action that started the move, not to
// the domain-data routing that followed it. Deadline and eligibility only
// ever apply to the node the case actually comes to rest on.
//
// If routing cannot find a matching route — a route's guard errors, or,
// defensively, a Definition that reached the engine without going through
// Validate somehow lacks an unguarded default — Move fails (with the
// guard's own error, or ErrNoRoute) and leaves the case and its history
// completely untouched: the whole hop chain is planned in memory, in
// planMove, before anything is written.
//
// When the hop chain ends on a terminal node, the automatic EventClosed
// Move appends takes its Action from the LAST hop in the chain: the final
// automatic decision-node route's label when the human-driven transition
// landed on a decision node and routing continued from there, or the
// human-driven transition's own Action (in.Action) when it landed on the
// terminal node directly with no further hops. EventClosed.Action never
// repeats in.Action once a route hop followed it — it describes how the
// case actually reached the terminal node, not what the human asked for.
//
// Effects: once the case update and every event are persisted, Move calls
// the EffectSink's Dispatch (when one is set) exactly once, with the final
// case and exactly the events this call appended, in Seq order. A Dispatch
// error fails Move after its writes went through the caller's db, so the
// caller must roll back its transaction. A Move that fails earlier never
// dispatches.
//
// Concurrency: the case is written through Repository.Update, a
// compare-and-swap on Case.Revision, BEFORE any event is appended. If
// another writer changed the case after Move read it, Move fails with
// ErrConflict and appends no event; the caller must roll back its
// transaction (a guard may already have written to it) and may retry from a
// fresh read. The check only sees writers that advance Case.Revision: during
// a rolling deploy, a process still running a vogel release from before the
// revision column overwrites cases without advancing it, so its writes can
// be lost undetected until every such process is replaced (see
// workflow/postgres Repository.Update for the deploy order).
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
	if err := e.checkEffectDB(d, db); err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
	}

	transition, found := findTransition(d.TransitionsFrom(c.State), in.Action)
	if !found {
		return nil, fmt.Errorf("workflow: move case %s: action %q from state %q: %w", c.ID, in.Action, c.State, ErrInvalidTransition)
	}

	// Enforce the transition's comment policy before any state change or
	// guard call, so a rejected comment never mutates the case and never
	// triggers a guard's own side effects.
	comment := strings.TrimSpace(in.Comment)
	if err := checkComment(transition.Comment, comment); err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
	}

	// A return transition is checked next, before the guard: the case's
	// event history is read only for a transition that actually needs it.
	if transition.Return {
		occupied, err := e.occupiedStates(ctx, db, in.CaseID)
		if err != nil {
			return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
		}
		if !occupied[transition.To] {
			return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, ErrReturnNotVisited)
		}
	}

	if transition.Guard != "" {
		fn, err := e.lookupGuard(transition.Guard)
		if err != nil {
			return nil, err
		}
		ok, err := fn(ctx, db, *c)
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

	hops, restNode, err := e.planMove(ctx, db, d, *c, c.State, in.Action, comment, destNode)
	if err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
	}

	now := e.now()
	c.State = restNode.ID
	c.AssignedTo = ""
	c.DeadlineAt = nil
	if restNode.Deadline > 0 {
		deadline := now.Add(restNode.Deadline)
		c.DeadlineAt = &deadline
	}

	terminated := restNode.Terminal
	if terminated {
		c.Status = StatusClosed
		c.ClosedAt = &now
		c.DeadlineAt = nil
	}

	if err := e.repo.Update(ctx, db, c); err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
	}

	appended := make([]Event, 0, len(hops)+1)
	for _, h := range hops {
		evt, err := e.appendEvent(ctx, db, c.ID, EventMoved, h.fromState, h.toState, h.action, in.ActorID, h.comment, now)
		if err != nil {
			return nil, fmt.Errorf("workflow: move case %s: append event: %w", c.ID, err)
		}
		appended = append(appended, evt)
	}

	if terminated {
		// The automatic close event shares the last hop's Action and the
		// original ActorID but never repeats any comment: the comment
		// belongs to the human-driven hop's own EventMoved record, appended
		// above, so Comment is passed as "" here rather than reused.
		lastAction := hops[len(hops)-1].action
		evt, err := e.appendEvent(ctx, db, c.ID, EventClosed, restNode.ID, restNode.ID, lastAction, in.ActorID, "", now)
		if err != nil {
			return nil, fmt.Errorf("workflow: move case %s: append close event: %w", c.ID, err)
		}
		appended = append(appended, evt)
	}

	if err := e.dispatch(ctx, db, d, *c, appended); err != nil {
		return nil, fmt.Errorf("workflow: move case %s: dispatch effects: %w", c.ID, err)
	}

	return c, nil
}

// planMove computes the full chain of hops one Move produces, without
// writing anything: the initial human-driven hop from fromState to dest via
// action (carrying comment), followed by zero or more automatic
// decision-node hops, each resolved by evaluating that decision node's
// routes — via resolveRoute, in declaration order — against a snapshot of
// base with State set to the decision node currently being routed from. It
// returns once it reaches a task or terminal node — the node this Move must
// come to rest on — together with the ordered hops that led there.
//
// A route's guard may itself write to the caller's db (that write is the
// caller's responsibility to roll back on error, like any other guard); but
// planMove itself never touches the case or its event history, so a
// mid-chain routing failure leaves both exactly as they were.
func (e *Engine) planMove(ctx context.Context, db pgxtx.DBTX, d Definition, base Case, fromState, action, comment string, dest Node) ([]decisionHop, Node, error) {
	hops := []decisionHop{{fromState: fromState, toState: dest.ID, action: action, comment: comment}}
	cur := dest

	for cur.Kind == NodeDecision {
		if len(hops) >= maxDecisionHops {
			return nil, Node{}, ErrTooManyDecisionHops
		}

		snapshot := base
		snapshot.State = cur.ID
		route, err := e.resolveRoute(ctx, db, d.TransitionsFrom(cur.ID), snapshot)
		if err != nil {
			return nil, Node{}, err
		}

		next, ok := d.Node(route.To)
		if !ok {
			return nil, Node{}, fmt.Errorf("decision node %q: route %q: destination node %q not found in definition %q", cur.ID, route.Action, route.To, d.Key())
		}

		hops = append(hops, decisionHop{fromState: cur.ID, toState: next.ID, action: route.Action})
		cur = next
	}

	return hops, cur, nil
}

// resolveRoute evaluates routes — a decision node's outgoing transitions —
// in declaration order and returns the first whose guard (if any) evaluates
// true. Definition.Validate guarantees exactly one unguarded default route,
// declared last, so against a validated Definition this only fails when a
// guarded route's own GuardFunc errors — which propagates here exactly like
// a guard error does elsewhere in this package, never collapsed into
// ErrNoRoute. It returns ErrNoRoute if every route is exhausted without a
// match, which should be unreachable against a validated Definition but
// guards against one that reached the engine without going through
// Validate.
func (e *Engine) resolveRoute(ctx context.Context, db pgxtx.DBTX, routes []Transition, c Case) (Transition, error) {
	for _, route := range routes {
		if route.Guard == "" {
			return route, nil
		}

		fn, err := e.lookupGuard(route.Guard)
		if err != nil {
			return Transition{}, err
		}
		ok, err := fn(ctx, db, c)
		if err != nil {
			return Transition{}, fmt.Errorf("decision node %q: route %q: guard %q: %w", c.State, route.Action, route.Guard, err)
		}
		if ok {
			return route, nil
		}
	}
	return Transition{}, ErrNoRoute
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
// Claim never calls the EffectSink: no effect can be declared on a claim.
// Like Move, the case update is a revision compare-and-swap performed before
// the event is appended: a concurrent change fails with ErrConflict and
// persists nothing, so two racing claimers can never both win.
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

	if _, err := e.appendEvent(ctx, db, c.ID, EventAssigned, c.State, c.State, "", actorID, "", e.now()); err != nil {
		return nil, fmt.Errorf("workflow: claim case %s: append event: %w", c.ID, err)
	}

	return c, nil
}

// Release clears the case's assignment. A concurrent change to the case
// fails with ErrConflict and persists nothing (see Claim). Like Claim, it
// never calls the EffectSink.
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

	if _, err := e.appendEvent(ctx, db, c.ID, EventUnassigned, c.State, c.State, "", actorID, "", e.now()); err != nil {
		return nil, fmt.Errorf("workflow: release case %s: append event: %w", c.ID, err)
	}

	return c, nil
}

// History returns every event recorded for caseID, ordered by Seq ascending
// — see Event.Seq for why Seq, rather than OccurredAt, is the ordering key.
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

// appendEvent computes the next Seq for caseID, appends an event, and
// returns the event as persisted (Move collects them for the EffectSink).
func (e *Engine) appendEvent(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, kind EventKind, fromState, toState, action, actorID, comment string, occurredAt time.Time) (Event, error) {
	seq, err := e.nextSeq(ctx, db, caseID)
	if err != nil {
		return Event{}, err
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
		Comment:    comment,
		OccurredAt: occurredAt,
	}
	if err := e.repo.AppendEvent(ctx, db, evt); err != nil {
		return Event{}, err
	}
	return *evt, nil
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
