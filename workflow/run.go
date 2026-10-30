package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

// maxGatewaySteps bounds how many splits and joins one call may pass through.
// Definition.Validate rejects every gateway-only cycle, so a validated
// Definition never comes close; like maxDecisionHops it only defends against
// a Definition that reached the engine without going through Validate.
const maxGatewaySteps = 256

// caseRun is one engine call's working copy of a case: the case row, its
// tokens and scopes, and the events the call plans to append. Every change
// is made in memory first; persist writes them only once planning succeeded,
// so a call that fails while planning leaves the case and its history
// untouched.
type caseRun struct {
	c      *Case
	d      Definition
	tokens []*Token
	scopes []*Scope
	// created and changed mark the tokens and scopes persist must insert or
	// update, by ID.
	created map[uuid.UUID]bool
	changed map[uuid.UUID]bool

	now   time.Time
	actor Actor
	// baseSeq is c.LastSeq as read; events get baseSeq+1, baseSeq+2, ...
	baseSeq      int64
	events       []Event
	joined       []JoinRecord
	gatewaySteps int
}

// loadRun reads c's tokens and scopes. An open case stored without any token
// (written by a binary that predates tokens, after migration 008) gets its
// root scope and root token synthesized here; they are persisted by the next
// write of the case, under its compare-and-swap.
//
// Deprecated behaviour: the synthesis is a compatibility shim, slated for
// removal in v0.21.0.
func (e *Engine) loadRun(ctx context.Context, db pgxtx.DBTX, c *Case, d Definition) (*caseRun, error) {
	tokens, err := e.repo.ListTokens(ctx, db, c.ID)
	if err != nil {
		return nil, err
	}
	scopes, err := e.repo.ListScopes(ctx, db, c.ID)
	if err != nil {
		return nil, err
	}
	run := &caseRun{
		c: c, d: d,
		created: make(map[uuid.UUID]bool),
		changed: make(map[uuid.UUID]bool),
		baseSeq: c.LastSeq,
	}
	for i := range scopes {
		run.scopes = append(run.scopes, &scopes[i])
	}
	for i := range tokens {
		run.tokens = append(run.tokens, &tokens[i])
	}
	if len(tokens) == 0 && c.Status == StatusOpen {
		if err := e.synthesizeRoot(ctx, db, run); err != nil {
			return nil, err
		}
	}
	run.fillEligibility()
	return run, nil
}

// fillEligibility sets the stored eligibility of every active token that has
// none while its node declares one: tokens migration 008 materialized, whose
// eligibility only the Definition knows. The next write of the case persists
// it (see Engine.RefreshEligibility).
func (r *caseRun) fillEligibility() {
	for _, t := range r.tokens {
		if t.Status != TokenActive || !t.Eligible.IsZero() {
			continue
		}
		if n, ok := r.d.Node(t.Node); ok && !n.Eligible.IsZero() {
			t.Eligible = resolvedEligibility(n, r.c.Unit)
			r.touch(t.ID)
		}
	}
}

// synthesizeRoot adds the root scope (when missing) and the root token of a
// case stored without tokens, at c.State with its assignment and deadline.
func (e *Engine) synthesizeRoot(ctx context.Context, db pgxtx.DBTX, run *caseRun) error {
	c := run.c
	entered := int64(1)
	events, err := e.listEvents(ctx, db, c.ID)
	if err != nil {
		return err
	}
	for i := range events {
		if (events[i].Kind == EventOpened || events[i].Kind == EventMoved) && events[i].ToState == c.State {
			entered = events[i].Seq
		}
	}
	if run.scope(c.ID) == nil {
		root := &Scope{ID: c.ID, CaseID: c.ID, Expected: 1, Status: ScopeOpen, OpenedSeq: 1}
		run.scopes = append(run.scopes, root)
		run.created[root.ID] = true
	}
	tok := &Token{
		ID: c.ID, CaseID: c.ID, ScopeID: c.ID, Node: c.State, Status: TokenActive,
		AssignedTo: c.AssignedTo, DeadlineAt: c.DeadlineAt,
		EnteredSeq: entered, EnteredAt: c.OpenedAt,
	}
	if n, ok := run.d.Node(c.State); ok {
		tok.Eligible = resolvedEligibility(n, c.Unit)
	}
	run.tokens = append(run.tokens, tok)
	run.created[tok.ID] = true
	return nil
}

// resolvedEligibility is n's eligibility with ResolveUnit applied against the
// case's unit, or the zero Eligibility when n declares none.
func resolvedEligibility(n Node, caseUnit string) Eligibility {
	if n.Eligible.IsZero() {
		return Eligibility{}
	}
	return Eligibility{Position: n.Eligible.Position, Unit: n.Eligible.ResolveUnit(caseUnit)}
}

// begin starts planning writes made at now by actor.
func (r *caseRun) begin(now time.Time, actor Actor) {
	r.now, r.actor = now, actor
}

func (r *caseRun) scope(id uuid.UUID) *Scope {
	for _, s := range r.scopes {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func (r *caseRun) token(id uuid.UUID) *Token {
	for _, t := range r.tokens {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// active returns the active tokens, in EnteredSeq order.
func (r *caseRun) active() []*Token {
	var out []*Token
	for _, t := range r.tokens {
		if t.Status == TokenActive {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EnteredSeq < out[j].EnteredSeq })
	return out
}

// pick returns the active token id names; uuid.Nil names the case's only
// active token. It fails with ErrTokenRequired when id is nil and several
// tokens are active, and ErrTokenNotFound when no active token matches.
func (r *caseRun) pick(id uuid.UUID) (*Token, error) {
	active := r.active()
	if id == uuid.Nil {
		switch len(active) {
		case 1:
			return active[0], nil
		case 0:
			return nil, ErrTokenNotFound
		default:
			return nil, ErrTokenRequired
		}
	}
	for _, t := range active {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, fmt.Errorf("%w: token %s", ErrTokenNotFound, id)
}

// snapshot is the Case a guard sees while tok is evaluated: the case with
// State, AssignedTo and DeadlineAt taken from tok and State set to node.
func (r *caseRun) snapshot(tok *Token, node string) Case {
	c := *r.c
	c.State = node
	c.AssignedTo = tok.AssignedTo
	c.DeadlineAt = tok.DeadlineAt
	return c
}

func (r *caseRun) addToken(t *Token) {
	r.tokens = append(r.tokens, t)
	r.created[t.ID] = true
}

func (r *caseRun) touch(id uuid.UUID) {
	if !r.created[id] {
		r.changed[id] = true
	}
}

// emit plans evt as the call's next event, assigning its Seq, time and
// actor, and returns the Seq.
func (r *caseRun) emit(evt *Event) int64 {
	evt.Seq = r.baseSeq + int64(len(r.events)) + 1
	evt.OccurredAt = r.now
	evt.Actor = r.actor
	r.events = append(r.events, *evt)
	return evt.Seq
}

// end marks tok as no longer live, ended by the event at seq.
func (r *caseRun) end(tok *Token, status TokenStatus, seq int64) {
	now := r.now
	tok.Status = status
	tok.EndedSeq = seq
	tok.EndedAt = &now
	tok.AssignedTo = ""
	tok.DeadlineAt = nil
	tok.ExpiredDeadline = nil
	r.touch(tok.ID)
}

// route moves tok from its node along tr and keeps routing automatically —
// through decision nodes, splits and joins — until tok rests on a task node,
// waits at a join, is consumed by a split, or closes the case. comment goes
// on the first hop only.
func (e *Engine) route(ctx context.Context, db pgxtx.DBTX, run *caseRun, tok *Token, tr Transition, comment string) error {
	hops := 0
	for {
		next, ok := run.d.Node(tr.To)
		if !ok {
			return fmt.Errorf("transition %q: destination node %q not found in definition %q", tr.Action, tr.To, run.d.Key())
		}
		seq := run.emit(&Event{
			Kind: EventMoved, TokenID: tok.ID, ScopeID: tok.ScopeID,
			FromState: tok.Node, ToState: next.ID, Action: tr.Action, Comment: comment,
		})
		hops++
		comment = ""
		tok.Node = next.ID
		tok.EnteredSeq, tok.EnteredAt = seq, run.now
		tok.AssignedTo, tok.DeadlineAt, tok.ExpiredDeadline, tok.Eligible = "", nil, nil, Eligibility{}
		run.touch(tok.ID)

		switch {
		case next.Kind == NodeDecision:
			if hops >= maxDecisionHops {
				return ErrTooManyDecisionHops
			}
			route, err := e.resolveRoute(ctx, db, run.d.TransitionsFrom(next.ID), run.snapshot(tok, next.ID))
			if err != nil {
				return err
			}
			tr = route
		case next.Kind.IsSplit():
			return e.split(ctx, db, run, tok, next)
		case next.Kind == NodeJoin:
			return e.arrive(ctx, db, run, tok, next)
		case next.Terminal:
			return run.closeCase(tok, next, tr.Action)
		default:
			tok.Status = TokenActive
			tok.Eligible = resolvedEligibility(next, run.c.Unit)
			deadline, err := e.deadlineFor(ctx, next, run.now, *run.c, *tok)
			if err != nil {
				return err
			}
			tok.DeadlineAt = deadline
			return nil
		}
	}
}

// gatewayStep counts one split or join against maxGatewaySteps.
func (r *caseRun) gatewayStep() error {
	r.gatewaySteps++
	if r.gatewaySteps > maxGatewaySteps {
		return ErrTooManyDecisionHops
	}
	return nil
}

// split consumes tok at the split node, opens a scope for the routes it takes
// and routes one new token along each of them.
func (e *Engine) split(ctx context.Context, db pgxtx.DBTX, run *caseRun, tok *Token, node Node) error {
	if err := run.gatewayStep(); err != nil {
		return err
	}
	routes := run.d.TransitionsFrom(node.ID)
	var taken []Transition
	if node.Kind == NodeParallel {
		taken = routes
	} else {
		snapshot := run.snapshot(tok, node.ID)
		var fallback []Transition
		for _, r := range routes {
			if r.Guard == "" {
				fallback = append(fallback, r)
				continue
			}
			fn, err := e.lookupGuard(r.Guard)
			if err != nil {
				return err
			}
			ok, err := fn(ctx, db, snapshot)
			if err != nil {
				return fmt.Errorf("inclusive split %q: route %q: guard %q: %w", node.ID, r.Action, r.Guard, err)
			}
			if ok {
				taken = append(taken, r)
			}
		}
		if len(taken) == 0 {
			taken = fallback
		}
	}
	if len(taken) == 0 {
		return fmt.Errorf("split %q: %w", node.ID, ErrNoRoute)
	}

	parent := tok.ScopeID
	scope := &Scope{
		ID: uuid.New(), CaseID: run.c.ID, ParentID: &parent,
		SplitNode: node.ID, JoinNode: node.Join, Expected: len(taken), Status: ScopeOpen,
	}
	branches := make([]*Token, len(taken))
	ids := make([]uuid.UUID, len(taken))
	for i := range taken {
		branches[i] = &Token{ID: uuid.New(), CaseID: run.c.ID, ScopeID: scope.ID, Node: node.ID, Status: TokenActive}
		ids[i] = branches[i].ID
	}
	detail, err := marshalDetail(SplitDetail{Scope: scope.ID, SplitNode: node.ID, Tokens: ids})
	if err != nil {
		return err
	}
	seq := run.emit(&Event{
		Kind: EventSplit, TokenID: tok.ID, ScopeID: tok.ScopeID,
		FromState: node.ID, ToState: node.ID, Detail: detail,
	})
	run.end(tok, TokenConsumed, seq)
	scope.OpenedSeq = seq
	run.scopes = append(run.scopes, scope)
	run.created[scope.ID] = true

	for i, b := range branches {
		run.addToken(b)
		if err := e.route(ctx, db, run, b, taken[i], ""); err != nil {
			return err
		}
	}
	return nil
}

// arrive parks tok at its scope's join and fires the join when it completes.
func (e *Engine) arrive(ctx context.Context, db pgxtx.DBTX, run *caseRun, tok *Token, node Node) error {
	scope := run.scope(tok.ScopeID)
	if scope == nil || scope.JoinNode != node.ID {
		return fmt.Errorf("token %s arrived at join %q outside its block", tok.ID, node.ID)
	}
	tok.Status = TokenWaiting
	run.emit(&Event{
		Kind: EventWaiting, TokenID: tok.ID, ScopeID: tok.ScopeID,
		FromState: node.ID, ToState: node.ID,
	})
	return e.tryJoin(ctx, db, run, scope)
}

// tryJoin fires scope's join once every token it expects is waiting or
// cancelled and at least one is waiting: it consumes the waiting tokens,
// marks the scope joined, asks Config.JoinCheck, and routes one new token in
// the parent scope along the join's outgoing transition.
func (e *Engine) tryJoin(ctx context.Context, db pgxtx.DBTX, run *caseRun, scope *Scope) error {
	if scope.Status != ScopeOpen {
		return nil
	}
	var waiting []*Token
	cancelled := 0
	for _, t := range run.tokens {
		if t.ScopeID != scope.ID {
			continue
		}
		switch t.Status {
		case TokenWaiting:
			waiting = append(waiting, t)
		case TokenCancelled:
			cancelled++
		case TokenActive, TokenConsumed:
			// Still on its way, or a nested split consumed it: not arrived.
		}
	}
	if len(waiting) == 0 || len(waiting)+cancelled != scope.Expected {
		return nil
	}
	if err := run.gatewayStep(); err != nil {
		return err
	}
	join, ok := run.d.Node(scope.JoinNode)
	if !ok {
		return fmt.Errorf("join node %q not found in definition %q", scope.JoinNode, run.d.Key())
	}
	out := run.d.TransitionsFrom(join.ID)
	if len(out) != 1 {
		return fmt.Errorf("join node %q: has %d outgoing transitions, want 1", join.ID, len(out))
	}

	next := &Token{ID: uuid.New(), CaseID: run.c.ID, ScopeID: *scope.ParentID, Node: join.ID, Status: TokenActive}
	ids := make([]uuid.UUID, len(waiting))
	for i, t := range waiting {
		ids[i] = t.ID
	}
	detail, err := marshalDetail(JoinDetail{Scope: scope.ID, JoinNode: join.ID, Tokens: ids})
	if err != nil {
		return err
	}
	seq := run.emit(&Event{
		Kind: EventJoined, TokenID: next.ID, ScopeID: scope.ID,
		FromState: join.ID, ToState: join.ID, Detail: detail,
	})
	rec := JoinRecord{Tokens: make([]Token, len(waiting))}
	for i, t := range waiting {
		run.end(t, TokenConsumed, seq)
		rec.Tokens[i] = *t
	}
	scope.Status = ScopeJoined
	scope.ClosedSeq = seq
	run.touch(scope.ID)
	rec.Scope = *scope

	if e.joinCheck != nil {
		if err := e.joinCheck(ctx, db, rec); err != nil {
			return fmt.Errorf("%w: %w", ErrJoinRejected, err)
		}
	}
	run.joined = append(run.joined, rec)
	run.addToken(next)
	return e.route(ctx, db, run, next, out[0], "")
}

// closeCase consumes tok on the terminal node, closes the root scope and the
// case, and plans the EventClosed, whose Action is the last hop's.
func (r *caseRun) closeCase(tok *Token, node Node, action string) error {
	if tok.ScopeID != r.c.ID {
		return fmt.Errorf("token %s reached terminal node %q inside a block", tok.ID, node.ID)
	}
	seq := r.emit(&Event{
		Kind: EventClosed, TokenID: tok.ID, ScopeID: tok.ScopeID,
		FromState: node.ID, ToState: node.ID, Action: action,
	})
	r.end(tok, TokenConsumed, seq)
	if root := r.scope(r.c.ID); root != nil {
		root.Status = ScopeClosed
		root.ClosedSeq = seq
		r.touch(root.ID)
	}
	now := r.now
	r.c.Status = StatusClosed
	r.c.ClosedAt = &now
	r.c.State = node.ID
	return nil
}

// project sets the case's State, AssignedTo and DeadlineAt from its tokens
// while it is open: the only active token's node and assignee (empty with
// several), and the earliest active deadline. A closed or cancelled case
// keeps its State and has neither assignee nor deadline.
func (r *caseRun) project() {
	c := r.c
	if c.Status != StatusOpen {
		c.AssignedTo = ""
		c.DeadlineAt = nil
		return
	}
	active := r.active()
	c.State, c.AssignedTo, c.DeadlineAt = "", "", nil
	if len(active) == 1 {
		c.State = active[0].Node
		c.AssignedTo = active[0].AssignedTo
	}
	for _, t := range active {
		if t.DeadlineAt != nil && (c.DeadlineAt == nil || t.DeadlineAt.Before(*c.DeadlineAt)) {
			d := *t.DeadlineAt
			c.DeadlineAt = &d
		}
	}
}

// persist writes the run: the case through Repository.Update (the
// compare-and-swap that serializes every writer of the case), then the
// created and changed scopes and tokens, then the planned events in Seq
// order. It returns the events as appended.
func (e *Engine) persist(ctx context.Context, db pgxtx.DBTX, run *caseRun) ([]Event, error) {
	run.project()
	run.c.LastSeq = run.baseSeq + int64(len(run.events))
	if err := e.repo.Update(ctx, db, run.c, run.baseSeq); err != nil {
		return nil, err
	}
	if err := e.writeState(ctx, db, run); err != nil {
		return nil, err
	}
	appended := make([]Event, 0, len(run.events))
	for i := range run.events {
		evt, err := e.appendEvent(ctx, db, run.c, &run.events[i])
		if err != nil {
			return nil, fmt.Errorf("append %s event: %w", run.events[i].Kind, err)
		}
		appended = append(appended, evt)
	}
	return appended, nil
}

// writeState inserts the created and updates the changed scopes, then tokens
// (a token references its scope).
func (e *Engine) writeState(ctx context.Context, db pgxtx.DBTX, run *caseRun) error {
	for _, s := range run.scopes {
		switch {
		case run.created[s.ID]:
			if err := e.repo.CreateScope(ctx, db, s); err != nil {
				return fmt.Errorf("create scope: %w", err)
			}
		case run.changed[s.ID]:
			if err := e.repo.UpdateScope(ctx, db, s); err != nil {
				return fmt.Errorf("update scope: %w", err)
			}
		}
	}
	for _, t := range run.tokens {
		switch {
		case run.created[t.ID]:
			if err := e.repo.CreateToken(ctx, db, t); err != nil {
				return fmt.Errorf("create token: %w", err)
			}
		case run.changed[t.ID]:
			if err := e.repo.UpdateToken(ctx, db, t); err != nil {
				return fmt.Errorf("update token: %w", err)
			}
		}
	}
	return nil
}

// cancelDetail encodes reason as a CancelDetail.
func cancelDetail(reason string) (json.RawMessage, error) {
	return marshalDetail(CancelDetail{Reason: reason})
}
