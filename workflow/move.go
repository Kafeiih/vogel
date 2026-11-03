package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

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
	for i := range candidates {
		tr := &candidates[i]
		if tr.Return && !occupied[tr.To] {
			continue
		}
		if tr.Guard == "" {
			out = append(out, *tr)
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
			out = append(out, *tr)
		}
	}
	return out, nil
}

// hasReturnTransition reports whether any transition in candidates is a
// return transition. Available and Move both call this before reading the
// case's event history, so that read only ever happens when it can actually
// affect the result.
func hasReturnTransition(candidates []Transition) bool {
	for i := range candidates {
		if candidates[i].Return {
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
// (which may cascade into an outer join). A Node.JoinPolicy can close the round
// earlier, cancelling the branches still pending in this same call; a later
// Move on one of them fails with ErrTokenNotFound. Every token and scope change is
// written after the case's revision compare-and-swap, so two moves of one
// case never commit concurrently: the slower fails with ErrConflict and its
// retry sees the other's tokens. Assignment is always cleared on a move,
// since a new node means a new claim, unless the node a token comes to rest on
// declares a Node.AssignRule and Config.Assignees names a person.
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
	for i := range routes {
		route := &routes[i]
		if route.Guard == "" {
			return *route, nil
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
			return *route, nil
		}
	}
	return Transition{}, ErrNoRoute
}

// findTransition returns the transition in candidates matching action.
func findTransition(candidates []Transition, action string) (Transition, bool) {
	for i := range candidates {
		if candidates[i].Action == action {
			return candidates[i], true
		}
	}
	return Transition{}, false
}
