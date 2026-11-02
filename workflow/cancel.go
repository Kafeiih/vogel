package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

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
