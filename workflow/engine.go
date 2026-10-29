package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/reqctx"
)

// Config configures optional Engine behaviour.
type Config struct {
	// Now returns the current time. Nil uses time.Now — inject a fixed clock
	// in tests.
	Now func() time.Time
	// TraceIDs, if set, returns the trace and span IDs of the span carried by
	// a context, or empty strings when there is none; every appended event
	// records them as Event.TraceID and Event.SpanID. Nil leaves both empty.
	// It has the shape of postgres.Config.TraceIDs, so otel.SampledTraceIDs
	// fits it without this package depending on any tracing library.
	TraceIDs func(ctx context.Context) (traceID, spanID string)
}

// Engine runs a set of registered Definitions against a Repository: it
// opens cases, evaluates and applies transitions, tracks assignment, and
// records history.
type Engine struct {
	repo   Repository
	logger *slog.Logger
	now    func() time.Time
	// traceIDs is Config.TraceIDs; nil leaves Event.TraceID/SpanID empty.
	traceIDs func(ctx context.Context) (traceID, spanID string)

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
	// the EventOpened; Move: the EventStepSubmitted when MoveInput.Step is
	// set, the human-driven EventMoved, every automatic
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
		traceIDs:    cfg.TraceIDs,
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
	// Tenant scopes the case (see Case.Tenant).
	Tenant string
	// Actor is who opens the case.
	Actor Actor
	// ActorID is the deprecated alias of Actor: with Actor.ID empty it means
	// Actor{Kind: ActorHuman, ID: ActorID}; set to a different ID than
	// Actor.ID it fails the call with ErrInvalidActor.
	//
	// Deprecated: use Actor.
	ActorID string
}

// Open creates a case at the definition's start node and appends an
// EventOpened event as Seq 1. It is callable inside the caller's own
// transaction via db (see pgxtx.DBTX). When an EffectSink is set, Open then
// calls its Dispatch once with that event; a Dispatch error fails Open and
// the caller must roll back its transaction, discarding the new case.
func (e *Engine) Open(ctx context.Context, db pgxtx.DBTX, in OpenInput) (*Case, error) {
	res, err := e.OpenDetailed(ctx, db, in)
	if err != nil {
		return nil, err
	}
	return res.Case, nil
}

// OpenDetailed is Open, also returning the EventOpened it appended (see
// MoveResult).
func (e *Engine) OpenDetailed(ctx context.Context, db pgxtx.DBTX, in OpenInput) (*MoveResult, error) {
	actor, err := resolveActor(in.Actor, in.ActorID)
	if err != nil {
		return nil, fmt.Errorf("workflow: open case: %w", err)
	}
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
		Tenant:     in.Tenant,
		Domain:     in.Domain,
		ExternalID: in.ExternalID,
		Unit:       in.Unit,
		State:      start.ID,
		Status:     StatusOpen,
		OpenedAt:   now,
		// Open appends exactly one event, so Create persists the case
		// already pointing at it.
		LastSeq: 1,
	}
	if start.Deadline > 0 {
		deadline := now.Add(start.Deadline)
		c.DeadlineAt = &deadline
	}

	if err := e.repo.Create(ctx, db, c); err != nil {
		return nil, fmt.Errorf("workflow: open case: %w", err)
	}

	evt, err := e.appendEvent(ctx, db, c, &Event{
		Seq:        c.LastSeq,
		Kind:       EventOpened,
		ToState:    start.ID,
		Actor:      actor,
		OccurredAt: now,
	})
	if err != nil {
		return nil, fmt.Errorf("workflow: open case: append event: %w", err)
	}

	events := []Event{evt}
	if err := e.dispatch(ctx, db, d, *c, events); err != nil {
		return nil, fmt.Errorf("workflow: open case %s: dispatch effects: %w", c.ID, err)
	}

	return &MoveResult{Case: c, Events: events}, nil
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
	events, err := e.listEvents(ctx, db, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: resolve occupied states: %w", err)
	}

	occupied := make(map[string]bool, len(events))
	// Indexed rather than ranged by value: Event is large and only
	// Kind/FromState/ToState are read here, over a case history that grows
	// without bound.
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
	CaseID uuid.UUID
	Action string
	// Actor is who performs the move. Every event the move appends records
	// it.
	Actor Actor
	// ActorID is the deprecated alias of Actor (see OpenInput.ActorID).
	//
	// Deprecated: use Actor.
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
	// Step, when set, is the form submission this move carries: Move appends
	// an EventStepSubmitted with it as detail immediately before the human
	// EventMoved, in the same seq block. Nil appends no step event.
	Step *StepRecord
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
// the original MoveInput actor (see resolveActor) throughout. Only the first hop ever
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
// The compare-and-swap also checks the LastSeq Move read, so a RecordFailure
// committed in between (it advances LastSeq, not Revision) fails Move with
// ErrConflict the same way, before anything is written.
func (e *Engine) Move(ctx context.Context, db pgxtx.DBTX, in MoveInput) (*Case, error) {
	res, err := e.MoveDetailed(ctx, db, in)
	if err != nil {
		return nil, err
	}
	return res.Case, nil
}

// MoveDetailed is Move, also returning exactly the events it appended, in
// Seq order (the same slice the EffectSink receives): a consumer can stamp
// the Seq of the event that triggered its own write on that row.
func (e *Engine) MoveDetailed(ctx context.Context, db pgxtx.DBTX, in MoveInput) (*MoveResult, error) {
	actor, err := resolveActor(in.Actor, in.ActorID)
	if err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", in.CaseID, err)
	}
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

	// Every event this call appends gets its Seq from the case's LastSeq,
	// advanced here and persisted by the Update compare-and-swap below, so
	// two racing Moves can never both claim the same Seq block.
	var stepDetail json.RawMessage
	if in.Step != nil {
		if stepDetail, err = marshalDetail(*in.Step); err != nil {
			return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
		}
	}
	seq := c.LastSeq
	c.LastSeq += int64(len(hops))
	if terminated {
		c.LastSeq++
	}
	if in.Step != nil {
		c.LastSeq++
	}

	if err := e.repo.Update(ctx, db, c, seq); err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
	}

	appended := make([]Event, 0, len(hops)+2)
	if in.Step != nil {
		// The submission is recorded at the state it was made in, before the
		// human hop that leaves it.
		seq++
		evt, err := e.appendEvent(ctx, db, c, &Event{
			Seq: seq, Kind: EventStepSubmitted,
			FromState: hops[0].fromState, ToState: hops[0].fromState, Action: in.Action,
			Actor: actor, OccurredAt: now, Detail: stepDetail,
		})
		if err != nil {
			return nil, fmt.Errorf("workflow: move case %s: append step event: %w", c.ID, err)
		}
		appended = append(appended, evt)
	}
	for _, h := range hops {
		seq++
		evt, err := e.appendEvent(ctx, db, c, &Event{
			Seq: seq, Kind: EventMoved,
			FromState: h.fromState, ToState: h.toState, Action: h.action,
			Actor: actor, Comment: h.comment, OccurredAt: now,
		})
		if err != nil {
			return nil, fmt.Errorf("workflow: move case %s: append event: %w", c.ID, err)
		}
		appended = append(appended, evt)
	}

	if terminated {
		// The automatic close event shares the last hop's Action and the
		// original Actor but never repeats any comment: the comment
		// belongs to the human-driven hop's own EventMoved record, appended
		// above, so Comment is passed as "" here rather than reused.
		lastAction := hops[len(hops)-1].action
		seq++
		evt, err := e.appendEvent(ctx, db, c, &Event{
			Seq: seq, Kind: EventClosed,
			FromState: restNode.ID, ToState: restNode.ID, Action: lastAction,
			Actor: actor, OccurredAt: now,
		})
		if err != nil {
			return nil, fmt.Errorf("workflow: move case %s: append close event: %w", c.ID, err)
		}
		appended = append(appended, evt)
	}

	if err := e.dispatch(ctx, db, d, *c, appended); err != nil {
		return nil, fmt.Errorf("workflow: move case %s: dispatch effects: %w", c.ID, err)
	}

	return &MoveResult{Case: c, Events: appended}, nil
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

// Claim assigns the case to actorID, a person: it is ClaimWith with
// Actor{Kind: ActorHuman, ID: actorID}, so an empty actorID fails with
// ErrInvalidActor.
func (e *Engine) Claim(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, actorID string) (*Case, error) {
	return e.ClaimWith(ctx, db, ClaimInput{CaseID: caseID, Actor: Actor{Kind: ActorHuman, ID: actorID}})
}

// Release clears the case's assignment on behalf of actorID, a person: it is
// ReleaseWith with Actor{Kind: ActorHuman, ID: actorID} and no Reason.
func (e *Engine) Release(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, actorID string) (*Case, error) {
	return e.ReleaseWith(ctx, db, ReleaseInput{CaseID: caseID, Actor: Actor{Kind: ActorHuman, ID: actorID}})
}

// ClaimInput describes a claim: Actor becomes the case's assignee (its ID is
// stored as Case.AssignedTo), so Actor.ID is required whatever the kind. An
// empty Actor.Kind means ActorHuman.
type ClaimInput struct {
	CaseID uuid.UUID
	Actor  Actor
}

// ReleaseInput describes a release: Actor clears the assignment, for Reason.
// An empty Actor.Kind means ActorHuman.
type ReleaseInput struct {
	CaseID uuid.UUID
	Actor  Actor
	// Reason is the caller's free-text reason, recorded in the event's
	// AssignmentDetail.
	Reason string
}

// ClaimWith assigns the case to in.Actor.ID. Re-claiming by the same actor ID
// is a no-op success; claiming a case already assigned to someone else fails.
// An invalid Actor, or one without an ID (a claim must name its assignee),
// fails with ErrInvalidActor before anything is read. ClaimWith never calls the EffectSink: no effect can be declared on a claim.
// Like Move, the case update is a revision compare-and-swap performed before
// the event is appended: a concurrent change fails with ErrConflict and
// persists nothing, so two racing claimers can never both win.
func (e *Engine) ClaimWith(ctx context.Context, db pgxtx.DBTX, in ClaimInput) (*Case, error) {
	actor, err := resolveActor(in.Actor, "")
	if err != nil {
		return nil, fmt.Errorf("workflow: claim case %s: %w", in.CaseID, err)
	}
	if actor.ID == "" {
		return nil, fmt.Errorf("workflow: claim case %s: %w: the assignee needs an ID", in.CaseID, ErrInvalidActor)
	}
	c, err := e.repo.GetByID(ctx, db, in.CaseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: claim case: %w", err)
	}
	if c.Status == StatusClosed {
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, ErrCaseClosed)
	}
	if c.AssignedTo != "" {
		if c.AssignedTo == actor.ID {
			return c, nil
		}
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, ErrAlreadyAssigned)
	}

	detail, err := marshalDetail(AssignmentDetail{To: actor.ID})
	if err != nil {
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, err)
	}
	c.AssignedTo = actor.ID
	c.LastSeq++
	if err := e.repo.Update(ctx, db, c, c.LastSeq-1); err != nil {
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, err)
	}

	if _, err := e.appendEvent(ctx, db, c, &Event{
		Seq: c.LastSeq, Kind: EventAssigned,
		FromState: c.State, ToState: c.State, Actor: actor,
		OccurredAt: e.now(), Detail: detail,
	}); err != nil {
		return nil, fmt.Errorf("workflow: claim case %s: append event: %w", c.ID, err)
	}

	return c, nil
}

// ReleaseWith clears the case's assignment, recording the previous assignee
// and in.Reason in the event's AssignmentDetail. An invalid Actor fails with
// ErrInvalidActor before anything is read. A concurrent change to the case
// fails with ErrConflict and persists nothing (see ClaimWith). Like
// ClaimWith, it never calls the EffectSink.
func (e *Engine) ReleaseWith(ctx context.Context, db pgxtx.DBTX, in ReleaseInput) (*Case, error) {
	actor, err := resolveActor(in.Actor, "")
	if err != nil {
		return nil, fmt.Errorf("workflow: release case %s: %w", in.CaseID, err)
	}
	c, err := e.repo.GetByID(ctx, db, in.CaseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: release case: %w", err)
	}
	if c.AssignedTo == "" {
		return nil, fmt.Errorf("workflow: release case %s: %w", c.ID, ErrNotAssigned)
	}

	detail, err := marshalDetail(AssignmentDetail{From: c.AssignedTo, Reason: in.Reason})
	if err != nil {
		return nil, fmt.Errorf("workflow: release case %s: %w", c.ID, err)
	}
	c.AssignedTo = ""
	c.LastSeq++
	if err := e.repo.Update(ctx, db, c, c.LastSeq-1); err != nil {
		return nil, fmt.Errorf("workflow: release case %s: %w", c.ID, err)
	}

	if _, err := e.appendEvent(ctx, db, c, &Event{
		Seq: c.LastSeq, Kind: EventUnassigned,
		FromState: c.State, ToState: c.State, Actor: actor,
		OccurredAt: e.now(), Detail: detail,
	}); err != nil {
		return nil, fmt.Errorf("workflow: release case %s: append event: %w", c.ID, err)
	}

	return c, nil
}

// History returns every event recorded for caseID, ordered by Seq ascending
// — see Event.Seq for why Seq, rather than OccurredAt, is the ordering key.
func (e *Engine) History(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Event, error) {
	events, err := e.listEvents(ctx, db, caseID)
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

// appendEvent appends evt, whose Seq, Actor and payload the caller set, to c:
// it stamps c's ID and Tenant, a new ID, CurrentEventSchema, ActorID mirrored
// from Actor.ID, the request ID and trace/span IDs of ctx, and an empty
// Detail object when it has none. It returns the event as persisted (Open
// and Move collect them for the EffectSink and MoveResult).
func (e *Engine) appendEvent(ctx context.Context, db pgxtx.DBTX, c *Case, evt *Event) (Event, error) {
	evt.ID = uuid.New()
	evt.CaseID = c.ID
	evt.Tenant = c.Tenant
	evt.ActorID = evt.Actor.ID
	evt.RequestID = reqctx.RequestIDFromContext(ctx)
	if e.traceIDs != nil {
		evt.TraceID, evt.SpanID = e.traceIDs(ctx)
	}
	evt.SchemaVersion = CurrentEventSchema
	if len(evt.Detail) == 0 {
		evt.Detail = emptyDetail()
	}
	if err := e.repo.AppendEvent(ctx, db, evt); err != nil {
		return Event{}, err
	}
	return *evt, nil
}

// listEvents reads caseID's history through the repository and upcasts every
// event to the current schema. An event newer than CurrentEventSchema fails
// the read with ErrUnsupportedEventSchema rather than being returned with
// fields this binary would drop.
func (e *Engine) listEvents(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Event, error) {
	events, err := e.repo.ListEvents(ctx, db, caseID)
	if err != nil {
		return nil, err
	}
	for i := range events {
		if events[i].SchemaVersion > CurrentEventSchema {
			return nil, fmt.Errorf("case %s event seq %d: schema version %d (this binary knows up to %d): %w",
				caseID, events[i].Seq, events[i].SchemaVersion, CurrentEventSchema, ErrUnsupportedEventSchema)
		}
		upcastEvent(&events[i])
	}
	return events, nil
}

// upcastEvent fills the defaults an event written with an older schema lacks.
// Stored rows are never rewritten; this runs on every read. A version of 0
// comes from a repository that does not store the version and reads as 1.
// An event without an actor kind (a version-1 row read from a repository that
// does not store the kind) reads as ActorUnspecified, and Actor.ID and the
// deprecated ActorID fill each other. It updates evt in place.
func upcastEvent(evt *Event) {
	if evt.SchemaVersion < 1 {
		evt.SchemaVersion = 1
	}
	if evt.Actor.Kind == "" {
		evt.Actor.Kind = ActorUnspecified
	}
	if evt.Actor.ID == "" {
		evt.Actor.ID = evt.ActorID
	}
	evt.ActorID = evt.Actor.ID
	if len(evt.Detail) == 0 {
		evt.Detail = emptyDetail()
	}
}

// resolveActor returns the Actor an input records, applying the deprecated
// actorID alias: an empty actor.ID takes actorID, and an empty Kind means
// ActorHuman. It fails with ErrInvalidActor when actor.ID and actorID are
// both set and differ, when the kind is unknown or ActorUnspecified (a
// read-only kind), and when an ActorSystem has no ID.
func resolveActor(actor Actor, actorID string) (Actor, error) {
	switch {
	case actor.ID == "":
		actor.ID = actorID
	case actorID != "" && actorID != actor.ID:
		return Actor{}, fmt.Errorf("%w: Actor.ID %q differs from ActorID %q", ErrInvalidActor, actor.ID, actorID)
	}
	if actor.Kind == "" {
		actor.Kind = ActorHuman
	}
	if !actor.Kind.Valid() || actor.Kind == ActorUnspecified {
		return Actor{}, fmt.Errorf("%w: kind %q", ErrInvalidActor, actor.Kind)
	}
	if actor.Kind == ActorSystem && actor.ID == "" {
		return Actor{}, fmt.Errorf("%w: a system actor needs an ID", ErrInvalidActor)
	}
	return actor, nil
}

// emptyDetail returns a fresh `{}`, the Detail of a kind that carries none.
func emptyDetail() json.RawMessage {
	return json.RawMessage(`{}`)
}

// marshalDetail encodes a kind's detail type as Event.Detail.
func marshalDetail(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode event detail: %w", err)
	}
	return b, nil
}

// FailureInput describes a rejected move to record (see RecordFailure).
type FailureInput struct {
	CaseID uuid.UUID
	Actor  Actor
	Action string
	Kind   FailureKind
	Detail json.RawMessage
}

// RecordFailure appends a record of a rejected move to the case: an
// EventNoRoute for FailureNoRoute, an EventFailed for every other kind, with
// a FailureDetail carrying in.Kind and in.Detail. It never changes the case's
// state, assignment or Revision; it only takes the next Seq, through
// Repository.AdvanceLastSeq, which locks the case row, so it serializes with
// every other writer of the case.
//
// The move that failed ran in a transaction the caller must roll back first:
// RecordFailure runs in the caller's next transaction (db), so the record
// survives the rollback. FailureKindOf maps the engine's sentinel errors to
// a FailureKind. An unknown kind, a Detail that is not a JSON object or an
// invalid Actor fails with ErrInvalidFailure or ErrInvalidActor before
// anything is written.
//
// A failure recorded while another call holds a stale read of the same case
// advances LastSeq but not Revision; that call's Repository.Update compares
// the LastSeq it read too, so it fails with ErrConflict before writing
// anything, and it retries from a fresh read like any other concurrent
// change.
func (e *Engine) RecordFailure(ctx context.Context, db pgxtx.DBTX, in FailureInput) (Event, error) {
	if !in.Kind.Valid() {
		return Event{}, fmt.Errorf("workflow: record failure on case %s: %w: kind %q", in.CaseID, ErrInvalidFailure, in.Kind)
	}
	if len(in.Detail) > 0 && !isJSONObject(in.Detail) {
		return Event{}, fmt.Errorf("workflow: record failure on case %s: %w: detail must be a JSON object", in.CaseID, ErrInvalidFailure)
	}
	actor, err := resolveActor(in.Actor, "")
	if err != nil {
		return Event{}, fmt.Errorf("workflow: record failure on case %s: %w", in.CaseID, err)
	}
	detail, err := marshalDetail(FailureDetail{Kind: in.Kind, Detail: in.Detail})
	if err != nil {
		return Event{}, fmt.Errorf("workflow: record failure on case %s: %w", in.CaseID, err)
	}

	c, err := e.repo.AdvanceLastSeq(ctx, db, in.CaseID)
	if err != nil {
		return Event{}, fmt.Errorf("workflow: record failure on case %s: %w", in.CaseID, err)
	}

	kind := EventFailed
	if in.Kind == FailureNoRoute {
		kind = EventNoRoute
	}
	evt, err := e.appendEvent(ctx, db, c, &Event{
		Seq: c.LastSeq, Kind: kind,
		FromState: c.State, ToState: c.State, Action: in.Action,
		Actor: actor, OccurredAt: e.now(), Detail: detail,
	})
	if err != nil {
		return Event{}, fmt.Errorf("workflow: record failure on case %s: append event: %w", c.ID, err)
	}
	return evt, nil
}

// failureKinds maps each sentinel a rejected Move, Claim or Release can
// return to the FailureKind RecordFailure records it as.
var failureKinds = []struct {
	err  error
	kind FailureKind
}{
	{ErrNoRoute, FailureNoRoute},
	{ErrGuardRejected, FailureGuardRejected},
	{ErrConflict, FailureConflict},
	{ErrInvalidActor, FailureValidationFailed},
	{ErrCommentRequired, FailureCommentRejected},
	{ErrCommentNotAllowed, FailureCommentRejected},
	{ErrReturnNotVisited, FailureReturnNotVisited},
	{ErrInvalidTransition, FailureTransitionNotFound},
}

// FailureKindOf reports the FailureKind for err when it wraps one of the
// engine's rejected-move sentinels (ErrNoRoute, ErrGuardRejected,
// ErrConflict, ErrInvalidActor, ErrCommentRequired, ErrCommentNotAllowed,
// ErrReturnNotVisited, ErrInvalidTransition), and false for any other error,
// such as ErrCaseClosed or a database failure, which RecordFailure is not
// meant to record.
func FailureKindOf(err error) (FailureKind, bool) {
	for _, fk := range failureKinds {
		if errors.Is(err, fk.err) {
			return fk.kind, true
		}
	}
	return "", false
}

// isJSONObject reports whether raw is a valid JSON object.
func isJSONObject(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	return json.Unmarshal(raw, &obj) == nil && obj != nil
}

// Participated returns the distinct cases in which f.ActorID appears as the
// actor of any event (opened, moved, claimed, released, a step submission or
// a recorded failure), open or closed, assigned to them or not, ordered by
// the actor's newest event in each case, newest first. It pages like Inbox
// (see ParticipationFilter.Page) and fails with ErrInvalidActor for an empty
// ActorID.
func (e *Engine) Participated(ctx context.Context, db pgxtx.DBTX, f ParticipationFilter) ([]Case, error) {
	if f.ActorID == "" {
		return nil, fmt.Errorf("workflow: participated: %w: ActorID is required", ErrInvalidActor)
	}
	if _, _, err := f.Page(); err != nil {
		return nil, fmt.Errorf("workflow: participated: %w", err)
	}
	cases, err := e.repo.ListParticipated(ctx, db, f)
	if err != nil {
		return nil, fmt.Errorf("workflow: participated: %w", err)
	}
	return cases, nil
}
