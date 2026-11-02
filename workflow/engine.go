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
	// Deadlines resolves Node.DeadlineRule when a token enters a node that
	// declares one. Nil means no definition may declare a deadline rule
	// (Engine.Register fails with ErrUnknownDeadlineRule).
	Deadlines DeadlineResolver
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
	// deadlines is Config.Deadlines.
	deadlines DeadlineResolver

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
// events each Open, Move, CancelToken or Expire appended.
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
	// Dispatch is called by Open, Move, CancelToken and Expire (when it takes
	// the node's OnExpire transition) exactly once, after all their
	// persistence succeeded, with the definition the case runs on,
	// the final case, and exactly the events that call appended, in Seq order
	// (Open: the EventOpened; Move: the EventStepSubmitted when MoveInput.Step
	// is set, the human-driven EventMoved, then every automatic event that
	// followed it: decision-node hops, splits and the branch tokens' hops,
	// waits, joins, and the EventClosed if the case closed). Each event names
	// its Event.TokenID. db is the exact value the caller passed to that
	// call, so whatever Dispatch writes commits or rolls back with the
	// caller's transaction. A returned error fails the call (wrapped); the caller
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
		deadlines:   cfg.Deadlines,
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
// sink's HasEffect reports false for that name. A definition declaring a
// Node.DeadlineRule fails with ErrUnknownDeadlineRule when Config.Deadlines
// is nil.
func (e *Engine) Register(d Definition) error {
	if err := d.Validate(); err != nil {
		return fmt.Errorf("workflow: register definition %q: %w", d.Key(), err)
	}
	if err := e.checkEffects(d); err != nil {
		return fmt.Errorf("workflow: register definition %q: %w", d.Key(), err)
	}
	if rules := d.DeadlineRules(); len(rules) > 0 && e.deadlines == nil {
		return fmt.Errorf("workflow: register definition %q: %w: no deadline resolver set for %s",
			d.Key(), ErrUnknownDeadlineRule, strings.Join(rules, ", "))
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

// dispatch hands the events one Open, Move, CancelToken or Expire appended
// to the EffectSink, if any. It is the engine's only contact with effects.
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

// maxDecisionHops bounds how many decision-node routing hops Move will
// follow in a single call before giving up. Definition.Validate rejects any
// cycle made up only of decision nodes, so a validated Definition can never
// come close to needing this many hops; the cap exists purely as a defense
// against a Definition that reached the engine without going through
// Validate (or a future bug in it) spinning Move forever.
const maxDecisionHops = 32

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
