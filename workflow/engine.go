package workflow

import (
	"bytes"
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
	// JoinCheck, if set, runs inside the transaction of every call that fires
	// a join, before anything is written, with the scope as joined and the
	// tokens it consumed. A non-nil error aborts the call with ErrJoinRejected
	// wrapping it, and nothing is persisted. Consumers use it to enforce how
	// branch-scoped data merges (reject a conflict, namespace or collect the
	// branches' values) without the engine reading domain data. Like a
	// GuardFunc it runs inside the caller's transaction and must not call the
	// network.
	JoinCheck func(ctx context.Context, db pgxtx.DBTX, j JoinRecord) error
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
	// joinCheck is Config.JoinCheck.
	joinCheck func(ctx context.Context, db pgxtx.DBTX, j JoinRecord) error

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
	// Dispatch is called by Open, Move and CancelToken exactly once, after
	// all their persistence succeeded, with the definition the case runs on,
	// the final case, and exactly the events that call appended, in Seq order
	// (Open: the EventOpened; Move: the EventStepSubmitted when MoveInput.Step
	// is set, the human-driven EventMoved, then every automatic event that
	// followed it: decision-node hops, splits and the branch tokens' hops,
	// waits, joins, and the EventClosed if the case closed). Each event names
	// its Event.TokenID. db is the exact value the caller passed to Open or Move, so
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
		joinCheck:   cfg.JoinCheck,
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
	// The root scope and the root token share the case ID, so a case that
	// never splits needs no other identifier.
	if err := e.repo.CreateScope(ctx, db, &Scope{
		ID: c.ID, CaseID: c.ID, Expected: 1, Status: ScopeOpen, OpenedSeq: 1,
	}); err != nil {
		return nil, fmt.Errorf("workflow: open case: create root scope: %w", err)
	}
	if err := e.repo.CreateToken(ctx, db, &Token{
		ID: c.ID, CaseID: c.ID, ScopeID: c.ID, Node: start.ID, Status: TokenActive,
		Eligible: resolvedEligibility(start, c.Unit), DeadlineAt: c.DeadlineAt,
		EnteredSeq: 1, EnteredAt: now,
	}); err != nil {
		return nil, fmt.Errorf("workflow: open case: create root token: %w", err)
	}

	evt, err := e.appendEvent(ctx, db, c, &Event{
		Seq:        c.LastSeq,
		Kind:       EventOpened,
		TokenID:    c.ID,
		ScopeID:    c.ID,
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

// Available returns the transitions leaving the current node of the case's
// only active token whose guard (if any) currently evaluates true; it is
// AvailableFor with a zero token ID, so a case with several active tokens
// fails with ErrTokenRequired. A closed or cancelled case offers none.
func (e *Engine) Available(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Transition, error) {
	return e.AvailableFor(ctx, db, caseID, uuid.Nil)
}

// AvailableFor returns the transitions leaving tokenID's current node whose
// guard (if any) currently evaluates true; a zero tokenID names the case's
// only active token (ErrTokenRequired with several, ErrTokenNotFound when the
// token is not active in the case). A return transition (Transition.Return)
// is filtered out unless the token's scope previously occupied its To node
// (see occupiedStates) — checked before the guard, exactly like Engine.Move
// orders the same two checks. A guard returning an error aborts the whole
// call. The case's event history is read at most once, and only when at
// least one candidate transition is a return.
func (e *Engine) AvailableFor(ctx context.Context, db pgxtx.DBTX, caseID, tokenID uuid.UUID) ([]Transition, error) {
	c, err := e.repo.GetByID(ctx, db, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: available transitions: %w", err)
	}
	if c.Status != StatusOpen && tokenID == uuid.Nil {
		return []Transition{}, nil
	}

	d, err := e.resolveDefinition(c.Definition, c.Version)
	if err != nil {
		return nil, err
	}
	run, err := e.loadRun(ctx, db, c, d)
	if err != nil {
		return nil, fmt.Errorf("workflow: available transitions: %w", err)
	}
	tok, err := run.pick(tokenID)
	if err != nil {
		return nil, fmt.Errorf("workflow: available transitions: %w", err)
	}

	candidates := d.TransitionsFrom(tok.Node)

	var occupied map[string]bool
	if hasReturnTransition(candidates) {
		occupied, err = e.occupiedStates(ctx, db, caseID, tok.ScopeID)
		if err != nil {
			return nil, fmt.Errorf("workflow: available transitions: %w", err)
		}
	}

	snapshot := run.snapshot(tok, tok.Node)
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
		ok, err := fn(ctx, db, snapshot)
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
// entered or left inside scopeID, derived entirely from its own event
// history: the node it was opened at (EventOpened.ToState, root scope only),
// plus the FromState and ToState of every EventMoved recorded in that scope —
// including decision nodes routed through, since each decision-node hop
// appends its own EventMoved. Only the scope's own events count: a return
// never crosses a block boundary (Definition.Validate), so another branch's
// or another scope's history must not make a return possible. It is the
// single source of truth Transition.Return checks against — never the
// Definition's graph reachability. A gateway can appear in this set, but that
// can never make a return into one possible: Definition.Validate rejects any
// return transition whose To is not a task node.
func (e *Engine) occupiedStates(ctx context.Context, db pgxtx.DBTX, caseID, scopeID uuid.UUID) (map[string]bool, error) {
	events, err := e.listEvents(ctx, db, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: resolve occupied states: %w", err)
	}

	occupied := make(map[string]bool, len(events))
	// Indexed rather than ranged by value: Event is large and only
	// Kind/FromState/ToState are read here, over a case history that grows
	// without bound.
	for i := range events {
		if events[i].ScopeID != scopeID {
			continue
		}
		switch events[i].Kind {
		case EventOpened:
			occupied[events[i].ToState] = true
		case EventMoved:
			occupied[events[i].FromState] = true
			occupied[events[i].ToState] = true
		default:
			// Every other kind carries a node the scope's EventMoved records
			// already counted (EventClosed shares the terminal node's hop,
			// assignment events the current node).
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

// MoveInput describes a transition to apply to a case.
type MoveInput struct {
	CaseID uuid.UUID
	// TokenID names the token to move. The zero value means the case's only
	// active token: with several active tokens Move fails with
	// ErrTokenRequired, and a token not active in the case fails with
	// ErrTokenNotFound.
	TokenID uuid.UUID
	Action  string
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
	// ExpectedRevision, when set, is the Case.Revision the caller's view of
	// the case was read at. The call fails with ErrConflict when the stored
	// revision differs, checked right after the case is read and before any
	// check of its state or any write, so an offline or long-form client
	// detects a stale view without reading the case again. Nil skips the
	// check (the compare-and-swap on write still applies).
	ExpectedRevision *int64
}

// Move applies the transition matching (token's node, Action) to the token
// MoveInput.TokenID names (the case's only active token when zero), then —
// if that lands on a decision node, a split or a join — keeps routing
// automatically, within this same call and the same db transaction, until
// every token it touched comes to rest on a task node, waits at a join, or
// closes the case on a terminal node. A token is never persisted resting on
// a decision node, a split or a join.
//
// A split consumes the arriving token, opens a Scope expecting one token per
// route taken (every route of a NodeParallel; every passing route of a
// NodeInclusive, or its default when none passes) and routes each new token
// in turn. A token arriving at its scope's join waits; once waiting plus
// cancelled tokens reach Scope.Expected, the join consumes the waiting ones,
// asks Config.JoinCheck, and one new token continues in the parent scope
// (which may cascade into an outer join). Every token and scope change is
// written after the case's revision compare-and-swap, so two moves of one
// case never commit concurrently: the slower fails with ErrConflict and its
// retry sees the other's tokens. Assignment is always cleared on a move,
// since a new node means a new claim.
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
// completely untouched: every hop, split and join is planned in memory
// before anything is written. A Config.JoinCheck rejection fails the same
// way, with ErrJoinRejected.
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
	if err := checkRevision(c, in.ExpectedRevision); err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
	}
	if c.Status != StatusOpen {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, ErrCaseClosed)
	}

	d, err := e.resolveDefinition(c.Definition, c.Version)
	if err != nil {
		return nil, err
	}
	if err := e.checkEffectDB(d, db); err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
	}
	run, err := e.loadRun(ctx, db, c, d)
	if err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
	}
	tok, err := run.pick(in.TokenID)
	if err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
	}

	transition, found := findTransition(d.TransitionsFrom(tok.Node), in.Action)
	if !found {
		return nil, fmt.Errorf("workflow: move case %s: action %q from state %q: %w", c.ID, in.Action, tok.Node, ErrInvalidTransition)
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
		occupied, err := e.occupiedStates(ctx, db, in.CaseID, tok.ScopeID)
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
		ok, err := fn(ctx, db, run.snapshot(tok, tok.Node))
		if err != nil {
			return nil, fmt.Errorf("workflow: move case %s: guard %q: %w", c.ID, transition.Guard, err)
		}
		if !ok {
			return nil, fmt.Errorf("workflow: move case %s: guard %q: %w", c.ID, transition.Guard, ErrGuardRejected)
		}
	}

	var stepDetail json.RawMessage
	if in.Step != nil {
		if stepDetail, err = marshalDetail(*in.Step); err != nil {
			return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
		}
	}

	// Plan every event and token change in memory first: a routing failure
	// (a route's guard erroring, no route, a rejected join) leaves the case
	// and its history untouched.
	run.begin(e.now(), actor)
	if in.Step != nil {
		// The submission is recorded at the node it was made in, before the
		// human hop that leaves it.
		run.emit(&Event{
			Kind: EventStepSubmitted, TokenID: tok.ID, ScopeID: tok.ScopeID,
			FromState: tok.Node, ToState: tok.Node, Action: in.Action, Detail: stepDetail,
		})
	}
	if err := e.route(ctx, db, run, tok, transition, comment); err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
	}

	appended, err := e.persist(ctx, db, run)
	if err != nil {
		return nil, fmt.Errorf("workflow: move case %s: %w", c.ID, err)
	}
	if err := e.dispatch(ctx, db, d, *c, appended); err != nil {
		return nil, fmt.Errorf("workflow: move case %s: dispatch effects: %w", c.ID, err)
	}

	return &MoveResult{Case: c, Events: appended, Joined: run.joined}, nil
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
// ReleaseWith with Actor{Kind: ActorHuman, ID: actorID} and no Reason, so it
// only releases actorID's own claim (ErrReasonRequired otherwise).
func (e *Engine) Release(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, actorID string) (*Case, error) {
	return e.ReleaseWith(ctx, db, ReleaseInput{CaseID: caseID, Actor: Actor{Kind: ActorHuman, ID: actorID}})
}

// ClaimInput describes a claim: Actor becomes the token's assignee (its ID
// is stored as Token.AssignedTo), so Actor.ID is required whatever the kind.
// An empty Actor.Kind means ActorHuman. TokenID names the token; zero means
// the case's only active token (see MoveInput.TokenID).
type ClaimInput struct {
	CaseID  uuid.UUID
	TokenID uuid.UUID
	Actor   Actor
	// ExpectedRevision works like MoveInput.ExpectedRevision.
	ExpectedRevision *int64
}

// ReleaseInput describes a release: Actor clears the token's assignment, for
// Reason. An empty Actor.Kind means ActorHuman. TokenID works like
// ClaimInput.TokenID.
type ReleaseInput struct {
	CaseID  uuid.UUID
	TokenID uuid.UUID
	Actor   Actor
	// Reason is the caller's free-text reason, recorded in the event's
	// AssignmentDetail. It is required (ErrReasonRequired when empty after
	// trimming) when Actor.ID is not the token's assignee, and optional for a
	// self-release.
	Reason string
	// ExpectedRevision works like MoveInput.ExpectedRevision.
	ExpectedRevision *int64
}

// ClaimWith assigns the token to in.Actor.ID. Re-claiming by the same actor
// ID is a no-op success; claiming a token already assigned to someone else
// fails with ErrAlreadyAssigned. Claims are per token: two branches of one
// case can be held by different people. An invalid Actor, or one without an
// ID (a claim must name its assignee), fails with ErrInvalidActor before
// anything is read. ClaimWith never calls the EffectSink: no effect can be
// declared on a claim. Like Move, the case update is a revision
// compare-and-swap performed before the event is appended: a concurrent
// change fails with ErrConflict and persists nothing, so two racing claimers
// can never both win.
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
	if err := checkRevision(c, in.ExpectedRevision); err != nil {
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, err)
	}
	if c.Status != StatusOpen {
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, ErrCaseClosed)
	}
	run, tok, err := e.loadToken(ctx, db, c, in.TokenID)
	if err != nil {
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, err)
	}
	if tok.AssignedTo != "" {
		if tok.AssignedTo == actor.ID {
			return c, nil
		}
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, ErrAlreadyAssigned)
	}

	detail, err := marshalDetail(AssignmentDetail{To: actor.ID})
	if err != nil {
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, err)
	}
	run.begin(e.now(), actor)
	tok.AssignedTo = actor.ID
	run.touch(tok.ID)
	run.emit(&Event{
		Kind: EventAssigned, TokenID: tok.ID, ScopeID: tok.ScopeID,
		FromState: tok.Node, ToState: tok.Node, Detail: detail,
	})
	if _, err := e.persist(ctx, db, run); err != nil {
		return nil, fmt.Errorf("workflow: claim case %s: %w", c.ID, err)
	}
	return c, nil
}

// ReleaseWith clears the token's assignment, recording the previous assignee
// and in.Reason in the event's AssignmentDetail. An invalid Actor fails with
// ErrInvalidActor before anything is read, a token nobody holds (or a case
// that is no longer open) with ErrNotAssigned, and a release of a token held
// by someone other than in.Actor.ID without a Reason with ErrReasonRequired. A concurrent change to
// the case fails with ErrConflict and persists nothing (see ClaimWith). Like
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
	if err := checkRevision(c, in.ExpectedRevision); err != nil {
		return nil, fmt.Errorf("workflow: release case %s: %w", c.ID, err)
	}
	if c.Status != StatusOpen {
		return nil, fmt.Errorf("workflow: release case %s: %w", c.ID, ErrNotAssigned)
	}
	run, tok, err := e.loadToken(ctx, db, c, in.TokenID)
	if err != nil {
		return nil, fmt.Errorf("workflow: release case %s: %w", c.ID, err)
	}
	if tok.AssignedTo == "" {
		return nil, fmt.Errorf("workflow: release case %s: %w", c.ID, ErrNotAssigned)
	}
	if actor.ID != tok.AssignedTo && strings.TrimSpace(in.Reason) == "" {
		return nil, fmt.Errorf("workflow: release case %s: %w: releasing someone else's token", c.ID, ErrReasonRequired)
	}

	detail, err := marshalDetail(AssignmentDetail{From: tok.AssignedTo, Reason: in.Reason})
	if err != nil {
		return nil, fmt.Errorf("workflow: release case %s: %w", c.ID, err)
	}
	run.begin(e.now(), actor)
	tok.AssignedTo = ""
	run.touch(tok.ID)
	run.emit(&Event{
		Kind: EventUnassigned, TokenID: tok.ID, ScopeID: tok.ScopeID,
		FromState: tok.Node, ToState: tok.Node, Detail: detail,
	})
	if _, err := e.persist(ctx, db, run); err != nil {
		return nil, fmt.Errorf("workflow: release case %s: %w", c.ID, err)
	}
	return c, nil
}

// ReassignInput describes an administrative reassignment: Actor moves the
// token to To, a person id, for Reason. TokenID works like ClaimInput.TokenID.
type ReassignInput struct {
	CaseID  uuid.UUID
	TokenID uuid.UUID
	// To is the new assignee's person id; it is required.
	To    string
	Actor Actor
	// Reason is required: Reassign fails with ErrReasonRequired when it is
	// empty after trimming. It is recorded trimmed.
	Reason string
	// ExpectedRevision works like MoveInput.ExpectedRevision.
	ExpectedRevision *int64
}

// Reassign assigns the token to in.To whoever holds it now (nobody
// included), appending an EventAssigned whose AssignmentDetail records the
// previous assignee, the new one and the reason. An empty Reason fails with
// ErrReasonRequired, and an empty To or an invalid Actor with
// ErrInvalidActor, before anything is read. Whether To holds the node's
// eligibility is the caller's check, as with ClaimWith: the engine never
// resolves people. Like ClaimWith it never calls the EffectSink, so a node's
// entry effects do not run again, and a concurrent change to the case fails
// with ErrConflict and persists nothing.
func (e *Engine) Reassign(ctx context.Context, db pgxtx.DBTX, in ReassignInput) (*Case, error) {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, fmt.Errorf("workflow: reassign case %s: %w", in.CaseID, ErrReasonRequired)
	}
	if in.To == "" {
		return nil, fmt.Errorf("workflow: reassign case %s: %w: the new assignee needs an ID", in.CaseID, ErrInvalidActor)
	}
	actor, err := resolveActor(in.Actor, "")
	if err != nil {
		return nil, fmt.Errorf("workflow: reassign case %s: %w", in.CaseID, err)
	}
	c, err := e.repo.GetByID(ctx, db, in.CaseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: reassign case: %w", err)
	}
	if err := checkRevision(c, in.ExpectedRevision); err != nil {
		return nil, fmt.Errorf("workflow: reassign case %s: %w", c.ID, err)
	}
	if c.Status != StatusOpen {
		return nil, fmt.Errorf("workflow: reassign case %s: %w", c.ID, ErrCaseClosed)
	}
	run, tok, err := e.loadToken(ctx, db, c, in.TokenID)
	if err != nil {
		return nil, fmt.Errorf("workflow: reassign case %s: %w", c.ID, err)
	}

	detail, err := marshalDetail(AssignmentDetail{From: tok.AssignedTo, To: in.To, Reason: reason})
	if err != nil {
		return nil, fmt.Errorf("workflow: reassign case %s: %w", c.ID, err)
	}
	run.begin(e.now(), actor)
	tok.AssignedTo = in.To
	run.touch(tok.ID)
	run.emit(&Event{
		Kind: EventAssigned, TokenID: tok.ID, ScopeID: tok.ScopeID,
		FromState: tok.Node, ToState: tok.Node, Detail: detail,
	})
	if _, err := e.persist(ctx, db, run); err != nil {
		return nil, fmt.Errorf("workflow: reassign case %s: %w", c.ID, err)
	}
	return c, nil
}

// loadToken resolves c's definition, loads its run and picks the active
// token tokenID names (see caseRun.pick).
func (e *Engine) loadToken(ctx context.Context, db pgxtx.DBTX, c *Case, tokenID uuid.UUID) (*caseRun, *Token, error) {
	d, err := e.resolveDefinition(c.Definition, c.Version)
	if err != nil {
		return nil, nil, err
	}
	run, err := e.loadRun(ctx, db, c, d)
	if err != nil {
		return nil, nil, err
	}
	tok, err := run.pick(tokenID)
	if err != nil {
		return nil, nil, err
	}
	return run, tok, nil
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

// Inbox returns one WorkItem per active token of an open case matching f,
// oldest case first, as before tokens existed, then the case's tokens in the
// order they entered their node, paged by f.Page. States,
// Eligibility, AssignedTo, Unassigned and Overdue apply to the token.
func (e *Engine) Inbox(ctx context.Context, db pgxtx.DBTX, f InboxFilter) ([]WorkItem, error) {
	items, err := e.repo.ListWorkItems(ctx, db, f)
	if err != nil {
		return nil, fmt.Errorf("workflow: inbox: %w", err)
	}
	return items, nil
}

// EligibilityFor returns the eligibility of the node the case's only active
// token rests on, with ResolveUnit already applied against the case's own
// unit; it is EligibilityForToken with a zero token ID. This is what a caller
// hands to its org-context service to resolve actual people. A closed or
// cancelled case answers for the node it ended on.
func (e *Engine) EligibilityFor(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) (Eligibility, error) {
	return e.EligibilityForToken(ctx, db, caseID, uuid.Nil)
}

// EligibilityForToken returns the eligibility of the node tokenID rests on,
// with ResolveUnit applied. A zero tokenID names the case's only active token
// (ErrTokenRequired with several, ErrTokenNotFound when the token is not
// active in the case).
func (e *Engine) EligibilityForToken(ctx context.Context, db pgxtx.DBTX, caseID, tokenID uuid.UUID) (Eligibility, error) {
	c, err := e.repo.GetByID(ctx, db, caseID)
	if err != nil {
		return Eligibility{}, fmt.Errorf("workflow: eligibility: %w", err)
	}

	d, err := e.resolveDefinition(c.Definition, c.Version)
	if err != nil {
		return Eligibility{}, err
	}

	state := c.State
	if c.Status == StatusOpen || tokenID != uuid.Nil {
		run, err := e.loadRun(ctx, db, c, d)
		if err != nil {
			return Eligibility{}, fmt.Errorf("workflow: eligibility: %w", err)
		}
		tok, err := run.pick(tokenID)
		if err != nil {
			return Eligibility{}, fmt.Errorf("workflow: eligibility: %w", err)
		}
		state = tok.Node
	}

	node, ok := d.Node(state)
	if !ok {
		return Eligibility{}, fmt.Errorf("workflow: eligibility: node %q not found in definition %q", state, d.Key())
	}

	return Eligibility{
		Position: node.Eligible.Position,
		Unit:     node.Eligible.ResolveUnit(c.Unit),
	}, nil
}

// Tokens returns every token of caseID — active, waiting, consumed and
// cancelled — ordered by EnteredSeq (oldest first), then ID.
func (e *Engine) Tokens(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Token, error) {
	c, err := e.repo.GetByID(ctx, db, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: tokens: %w", err)
	}
	d, err := e.resolveDefinition(c.Definition, c.Version)
	if err != nil {
		return nil, err
	}
	run, err := e.loadRun(ctx, db, c, d)
	if err != nil {
		return nil, fmt.Errorf("workflow: tokens: %w", err)
	}
	out := make([]Token, len(run.tokens))
	for i, t := range run.tokens {
		out[i] = *t
	}
	sortTokens(out)
	return out, nil
}

// RefreshEligibility writes the resolved eligibility of every active token of
// caseID that is stored without one, and reports whether it wrote anything.
// Tokens migration 008 materialized have none (a node's eligibility lives in
// the Definition), so they do not match InboxFilter.Eligibility until their
// case is written again; calling RefreshEligibility once per open case after
// the migration closes that gap. It appends no event, and like every write it
// is a compare-and-swap on the case revision (ErrConflict on a concurrent
// change).
func (e *Engine) RefreshEligibility(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) (bool, error) {
	c, err := e.repo.GetByID(ctx, db, caseID)
	if err != nil {
		return false, fmt.Errorf("workflow: refresh eligibility: %w", err)
	}
	if c.Status != StatusOpen {
		return false, nil
	}
	d, err := e.resolveDefinition(c.Definition, c.Version)
	if err != nil {
		return false, err
	}
	run, err := e.loadRun(ctx, db, c, d)
	if err != nil {
		return false, fmt.Errorf("workflow: refresh eligibility %s: %w", c.ID, err)
	}
	if len(run.changed) == 0 && len(run.created) == 0 {
		return false, nil
	}
	run.begin(e.now(), Actor{})
	if _, err := e.persist(ctx, db, run); err != nil {
		return false, fmt.Errorf("workflow: refresh eligibility %s: %w", c.ID, err)
	}
	return true, nil
}

// sortTokens orders tokens by EnteredSeq, then ID.
func sortTokens(tokens []Token) {
	sort.Slice(tokens, func(i, j int) bool {
		if tokens[i].EnteredSeq != tokens[j].EnteredSeq {
			return tokens[i].EnteredSeq < tokens[j].EnteredSeq
		}
		return tokens[i].ID.String() < tokens[j].ID.String()
	})
}

// CancelInput describes the cancellation of a whole case.
type CancelInput struct {
	CaseID uuid.UUID
	Actor  Actor
	// Reason is required: Cancel fails with ErrReasonRequired when it is
	// empty after trimming.
	Reason string
	// ExpectedRevision works like MoveInput.ExpectedRevision.
	ExpectedRevision *int64
}

// Cancel ends an open case without reaching a terminal node: it cancels
// every active and waiting token (one EventTokenCancelled each, in token
// order), sets every open scope to ScopeCancelled, sets the case to
// StatusCancelled with ClosedAt, and appends one EventCancelled. Both kinds
// carry the reason in a CancelDetail. A case that is not open fails with
// ErrCaseClosed. Cancel never calls the EffectSink.
func (e *Engine) Cancel(ctx context.Context, db pgxtx.DBTX, in CancelInput) (*Case, error) {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, fmt.Errorf("workflow: cancel case %s: %w", in.CaseID, ErrReasonRequired)
	}
	actor, err := resolveActor(in.Actor, "")
	if err != nil {
		return nil, fmt.Errorf("workflow: cancel case %s: %w", in.CaseID, err)
	}
	c, err := e.repo.GetByID(ctx, db, in.CaseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: cancel case: %w", err)
	}
	if err := checkRevision(c, in.ExpectedRevision); err != nil {
		return nil, fmt.Errorf("workflow: cancel case %s: %w", c.ID, err)
	}
	if c.Status != StatusOpen {
		return nil, fmt.Errorf("workflow: cancel case %s: %w", c.ID, ErrCaseClosed)
	}
	d, err := e.resolveDefinition(c.Definition, c.Version)
	if err != nil {
		return nil, err
	}
	run, err := e.loadRun(ctx, db, c, d)
	if err != nil {
		return nil, fmt.Errorf("workflow: cancel case %s: %w", c.ID, err)
	}
	detail, err := cancelDetail(reason)
	if err != nil {
		return nil, fmt.Errorf("workflow: cancel case %s: %w", c.ID, err)
	}

	run.begin(e.now(), actor)
	live := make([]Token, 0, len(run.tokens))
	for _, t := range run.tokens {
		if t.Status.Live() {
			live = append(live, *t)
		}
	}
	sortTokens(live)
	for i := range live {
		t := run.token(live[i].ID)
		seq := run.emit(&Event{
			Kind: EventTokenCancelled, TokenID: t.ID, ScopeID: t.ScopeID,
			FromState: t.Node, ToState: t.Node, Detail: detail,
		})
		run.end(t, TokenCancelled, seq)
	}
	seq := run.emit(&Event{
		Kind: EventCancelled, TokenID: c.ID, ScopeID: c.ID,
		FromState: c.State, ToState: c.State, Detail: detail,
	})
	for _, s := range run.scopes {
		if s.Status == ScopeOpen {
			s.Status = ScopeCancelled
			s.ClosedSeq = seq
			run.touch(s.ID)
		}
	}
	now := run.now
	c.Status = StatusCancelled
	c.ClosedAt = &now

	if _, err := e.persist(ctx, db, run); err != nil {
		return nil, fmt.Errorf("workflow: cancel case %s: %w", c.ID, err)
	}
	return c, nil
}

// CancelTokenInput describes the cancellation of one branch.
type CancelTokenInput struct {
	CaseID  uuid.UUID
	TokenID uuid.UUID
	Actor   Actor
	// Reason is required (see CancelInput.Reason).
	Reason string
}

// CancelToken cancels one active token (EventTokenCancelled with a
// CancelDetail) and counts it toward its scope's join: when every other
// token of the scope already waits, the join fires and routing continues in
// this same call, exactly like Move (effects dispatched, MoveResult.Joined
// reported). It fails with ErrLastBranch when the token is its scope's only
// live branch (a sibling running inside a nested block that has not joined
// counts as live), root scope included — an open case must never be left
// without a live token; use Cancel to end the whole case — with
// ErrTokenNotFound when the token is not active in the case, and with
// ErrCaseClosed when the case is not open.
func (e *Engine) CancelToken(ctx context.Context, db pgxtx.DBTX, in CancelTokenInput) (*MoveResult, error) {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, fmt.Errorf("workflow: cancel token %s: %w", in.TokenID, ErrReasonRequired)
	}
	if in.TokenID == uuid.Nil {
		return nil, fmt.Errorf("workflow: cancel token: %w: TokenID is required", ErrTokenNotFound)
	}
	actor, err := resolveActor(in.Actor, "")
	if err != nil {
		return nil, fmt.Errorf("workflow: cancel token %s: %w", in.TokenID, err)
	}
	c, err := e.repo.GetByID(ctx, db, in.CaseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: cancel token: %w", err)
	}
	if c.Status != StatusOpen {
		return nil, fmt.Errorf("workflow: cancel token %s: %w", in.TokenID, ErrCaseClosed)
	}
	d, err := e.resolveDefinition(c.Definition, c.Version)
	if err != nil {
		return nil, err
	}
	if err := e.checkEffectDB(d, db); err != nil {
		return nil, fmt.Errorf("workflow: cancel token %s: %w", in.TokenID, err)
	}
	run, err := e.loadRun(ctx, db, c, d)
	if err != nil {
		return nil, fmt.Errorf("workflow: cancel token %s: %w", in.TokenID, err)
	}
	tok, err := run.pick(in.TokenID)
	if err != nil {
		return nil, fmt.Errorf("workflow: cancel token %s: %w", in.TokenID, err)
	}
	// A sibling branch is live while it has a live token in the scope, or
	// while it runs inside a nested block whose scope has not joined yet.
	others := 0
	for _, t := range run.tokens {
		if t.ScopeID == tok.ScopeID && t.ID != tok.ID && t.Status.Live() {
			others++
		}
	}
	for _, s := range run.scopes {
		if s.ParentID != nil && *s.ParentID == tok.ScopeID && s.Status == ScopeOpen {
			others++
		}
	}
	if others == 0 {
		return nil, fmt.Errorf("workflow: cancel token %s: %w", in.TokenID, ErrLastBranch)
	}
	detail, err := cancelDetail(reason)
	if err != nil {
		return nil, fmt.Errorf("workflow: cancel token %s: %w", in.TokenID, err)
	}

	run.begin(e.now(), actor)
	seq := run.emit(&Event{
		Kind: EventTokenCancelled, TokenID: tok.ID, ScopeID: tok.ScopeID,
		FromState: tok.Node, ToState: tok.Node, Detail: detail,
	})
	run.end(tok, TokenCancelled, seq)
	if err := e.tryJoin(ctx, db, run, run.scope(tok.ScopeID)); err != nil {
		return nil, fmt.Errorf("workflow: cancel token %s: %w", in.TokenID, err)
	}

	appended, err := e.persist(ctx, db, run)
	if err != nil {
		return nil, fmt.Errorf("workflow: cancel token %s: %w", in.TokenID, err)
	}
	if err := e.dispatch(ctx, db, d, *c, appended); err != nil {
		return nil, fmt.Errorf("workflow: cancel token %s: dispatch effects: %w", in.TokenID, err)
	}
	return &MoveResult{Case: c, Events: appended, Joined: run.joined}, nil
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
// deprecated ActorID fill each other. An event without a token or scope (one
// written before tokens existed) reads as the root token in the root scope,
// both of which share the case ID. It updates evt in place.
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
	if evt.TokenID == uuid.Nil {
		evt.TokenID = evt.CaseID
	}
	if evt.ScopeID == uuid.Nil {
		evt.ScopeID = evt.CaseID
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

// checkRevision fails with ErrConflict when expected is set and differs from
// c.Revision (see MoveInput.ExpectedRevision).
func checkRevision(c *Case, expected *int64) error {
	if expected != nil && *expected != c.Revision {
		return fmt.Errorf("%w: expected revision %d, stored %d", ErrConflict, *expected, c.Revision)
	}
	return nil
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
	// TokenID names the token whose move failed, so the record lands in its
	// branch; zero records it at the case level (the root token).
	TokenID uuid.UUID
	Actor   Actor
	Action  string
	Kind    FailureKind
	Detail  json.RawMessage
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
// invalid Actor fails with ErrInvalidFailure or ErrInvalidActor, and a
// TokenID the case does not have with ErrTokenNotFound, before anything is
// written.
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

	var tok *Token
	if in.TokenID != uuid.Nil {
		tokens, err := e.repo.ListTokens(ctx, db, in.CaseID)
		if err != nil {
			return Event{}, fmt.Errorf("workflow: record failure on case %s: %w", in.CaseID, err)
		}
		for i := range tokens {
			if tokens[i].ID == in.TokenID {
				tok = &tokens[i]
			}
		}
		if tok == nil {
			return Event{}, fmt.Errorf("workflow: record failure on case %s: %w: token %s", in.CaseID, ErrTokenNotFound, in.TokenID)
		}
	}

	c, err := e.repo.AdvanceLastSeq(ctx, db, in.CaseID)
	if err != nil {
		return Event{}, fmt.Errorf("workflow: record failure on case %s: %w", in.CaseID, err)
	}

	kind := EventFailed
	if in.Kind == FailureNoRoute {
		kind = EventNoRoute
	}
	evt := &Event{
		Seq: c.LastSeq, Kind: kind, TokenID: c.ID, ScopeID: c.ID,
		FromState: c.State, ToState: c.State, Action: in.Action,
		Actor: actor, OccurredAt: e.now(), Detail: detail,
	}
	if tok != nil {
		evt.TokenID, evt.ScopeID = tok.ID, tok.ScopeID
		evt.FromState, evt.ToState = tok.Node, tok.Node
	}
	stored, err := e.appendEvent(ctx, db, c, evt)
	if err != nil {
		return Event{}, fmt.Errorf("workflow: record failure on case %s: append event: %w", c.ID, err)
	}
	return stored, nil
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

// VerifyChain checks caseID's hash chain: every chained event's digest must
// match its current content, and its PrevDigest must be the Digest of the
// event before it (nil after an unchained event, or for the first event).
// Unchained events before the first chained one, those stored before the
// repository chained events, are accepted as they are; an unchained event
// after it is not. It returns nil for an intact chain, a *ChainBrokenError
// (matching ErrChainBroken) for the first event that fails, and
// ErrCaseNotFound for an unknown case.
//
// The chain detects a stored event that was changed, removed or reordered
// without recomputing every digest after it. It cannot detect a rewrite that
// recomputes them, events removed from the end of the case, nor a rewrite
// that clears prev_digest and digest on every event of the case: VerifyChain
// cannot tell that from a case whose events all predate the chain, and
// accepts it. Comparing a Digest kept outside the database (an export, a
// signed log) is what detects them.
func (e *Engine) VerifyChain(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) error {
	if _, err := e.repo.GetByID(ctx, db, caseID); err != nil {
		return fmt.Errorf("workflow: verify chain: %w", err)
	}
	links, err := e.repo.ListEventDigests(ctx, db, caseID)
	if err != nil {
		return fmt.Errorf("workflow: verify chain: %w", err)
	}
	var prev []byte
	chained := false
	for _, l := range links {
		broken := func(reason string) error {
			return &ChainBrokenError{CaseID: caseID, Seq: l.Seq, Reason: reason}
		}
		if l.Digest == nil {
			if chained {
				return broken("event has no digest after the chain started")
			}
			if l.PrevDigest != nil {
				return broken("event has a prev_digest but no digest")
			}
			continue
		}
		chained = true
		if !bytes.Equal(l.PrevDigest, prev) {
			return broken("prev_digest does not match the previous event")
		}
		if !bytes.Equal(l.Recomputed, l.Digest) {
			return broken("digest does not match the event's content")
		}
		prev = l.Digest
	}
	return nil
}
