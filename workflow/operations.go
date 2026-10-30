package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

// Sentinel errors of the deadline operations.
var (
	// ErrNotOverdue reports an Expire of a token whose deadline has not
	// passed, or that has none.
	ErrNotOverdue = errors.New("workflow: token is not overdue")
	// ErrAlreadyExpired reports an Expire of a token whose current deadline
	// was already recorded as expired; a scheduler retry gets it instead of
	// a second EventExpired.
	ErrAlreadyExpired = errors.New("workflow: token deadline already expired")
)

// DeadlineDetail is the Event.Detail of EventDeadlineChanged: the token's
// deadline before and after the change (nil when there was, or now is, none)
// and the caller's reason.
type DeadlineDetail struct {
	From   *time.Time `json:"from"`
	To     *time.Time `json:"to"`
	Reason string     `json:"reason"`
}

// ExpiryDetail is the Event.Detail of EventExpired: the deadline that
// expired.
type ExpiryDetail struct {
	DeadlineAt time.Time `json:"deadline_at"`
}

// expiredCause is the Event.Detail of the EventMoved an Expire takes through
// Node.OnExpire.
type expiredCause struct {
	Cause string `json:"cause"`
}

// SetDeadlineInput describes a deadline change on one token. TokenID works
// like ClaimInput.TokenID.
type SetDeadlineInput struct {
	CaseID  uuid.UUID
	TokenID uuid.UUID
	// DeadlineAt is the new deadline; nil removes it.
	DeadlineAt *time.Time
	Actor      Actor
	// Reason is required: SetDeadline fails with ErrReasonRequired when it is
	// empty after trimming. It is recorded trimmed.
	Reason string
	// ExpectedRevision works like MoveInput.ExpectedRevision.
	ExpectedRevision *int64
}

// SetDeadline replaces the token's deadline with in.DeadlineAt (nil removes
// it) and appends an EventDeadlineChanged whose DeadlineDetail records the
// old and new deadline and the reason. It also clears the token's expired
// mark, so a token whose deadline was extended can expire again. An empty
// Reason fails with ErrReasonRequired and an invalid Actor with
// ErrInvalidActor, before anything is read; a case that is not open fails
// with ErrCaseClosed. It never calls the EffectSink, and a concurrent change
// to the case fails with ErrConflict and persists nothing.
func (e *Engine) SetDeadline(ctx context.Context, db pgxtx.DBTX, in SetDeadlineInput) (*Case, error) {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, fmt.Errorf("workflow: set deadline on case %s: %w", in.CaseID, ErrReasonRequired)
	}
	actor, err := resolveActor(in.Actor, "")
	if err != nil {
		return nil, fmt.Errorf("workflow: set deadline on case %s: %w", in.CaseID, err)
	}
	c, err := e.repo.GetByID(ctx, db, in.CaseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: set deadline: %w", err)
	}
	if err := checkRevision(c, in.ExpectedRevision); err != nil {
		return nil, fmt.Errorf("workflow: set deadline on case %s: %w", c.ID, err)
	}
	if c.Status != StatusOpen {
		return nil, fmt.Errorf("workflow: set deadline on case %s: %w", c.ID, ErrCaseClosed)
	}
	run, tok, err := e.loadToken(ctx, db, c, in.TokenID)
	if err != nil {
		return nil, fmt.Errorf("workflow: set deadline on case %s: %w", c.ID, err)
	}

	var deadline *time.Time
	if in.DeadlineAt != nil {
		d := *in.DeadlineAt
		deadline = &d
	}
	detail, err := marshalDetail(DeadlineDetail{From: tok.DeadlineAt, To: deadline, Reason: reason})
	if err != nil {
		return nil, fmt.Errorf("workflow: set deadline on case %s: %w", c.ID, err)
	}
	run.begin(e.now(), actor)
	tok.DeadlineAt = deadline
	tok.ExpiredDeadline = nil
	run.touch(tok.ID)
	run.emit(&Event{
		Kind: EventDeadlineChanged, TokenID: tok.ID, ScopeID: tok.ScopeID,
		FromState: tok.Node, ToState: tok.Node, Detail: detail,
	})
	if _, err := e.persist(ctx, db, run); err != nil {
		return nil, fmt.Errorf("workflow: set deadline on case %s: %w", c.ID, err)
	}
	return c, nil
}

// ExpireInput describes the expiry of one overdue token. TokenID works like
// ClaimInput.TokenID. Actor must be an ActorSystem: expiry is the consumer's
// scheduler's job, never a person's.
type ExpireInput struct {
	CaseID  uuid.UUID
	TokenID uuid.UUID
	Actor   Actor
}

// Expire records that the token's deadline passed: it appends an EventExpired
// (ExpiryDetail) once per deadline and marks the token expired. When the
// token's node declares Node.OnExpire, Expire then moves the token through
// that transition in the same call and transaction, exactly like Move with
// in.Actor (decision nodes, splits, joins and closing included, effects
// dispatched once with every appended event); that EventMoved's Detail is
// {"cause":"expired"}. The transition's CommentPolicy does not apply (the
// system has nothing to comment), but its Guard does: a rejection fails
// Expire with ErrGuardRejected and persists nothing. Without OnExpire the
// token stays where it is, visibly overdue, and nothing is dispatched.
//
// An Actor that is not a valid ActorSystem fails with ErrInvalidActor before
// anything is read. A token without a deadline, or whose deadline is after
// Config.Now, fails with ErrNotOverdue; one whose current deadline already
// expired fails with ErrAlreadyExpired, so a scheduler retry is idempotent.
// SetDeadline clears the mark. A case that is not open fails with
// ErrCaseClosed, and a concurrent change with ErrConflict.
func (e *Engine) Expire(ctx context.Context, db pgxtx.DBTX, in ExpireInput) (*MoveResult, error) {
	actor, err := resolveActor(in.Actor, "")
	if err != nil {
		return nil, fmt.Errorf("workflow: expire case %s: %w", in.CaseID, err)
	}
	if actor.Kind != ActorSystem {
		return nil, fmt.Errorf("workflow: expire case %s: %w: expiry needs a system actor, got %q", in.CaseID, ErrInvalidActor, actor.Kind)
	}
	c, err := e.repo.GetByID(ctx, db, in.CaseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: expire case: %w", err)
	}
	if c.Status != StatusOpen {
		return nil, fmt.Errorf("workflow: expire case %s: %w", c.ID, ErrCaseClosed)
	}
	d, err := e.resolveDefinition(c.Definition, c.Version)
	if err != nil {
		return nil, err
	}
	run, err := e.loadRun(ctx, db, c, d)
	if err != nil {
		return nil, fmt.Errorf("workflow: expire case %s: %w", c.ID, err)
	}
	tok, err := run.pick(in.TokenID)
	if err != nil {
		return nil, fmt.Errorf("workflow: expire case %s: %w", c.ID, err)
	}
	now := e.now()
	if tok.DeadlineAt == nil || tok.DeadlineAt.After(now) {
		return nil, fmt.Errorf("workflow: expire case %s: token %s: %w", c.ID, tok.ID, ErrNotOverdue)
	}
	if tok.ExpiredDeadline != nil && tok.ExpiredDeadline.Equal(*tok.DeadlineAt) {
		return nil, fmt.Errorf("workflow: expire case %s: token %s: %w", c.ID, tok.ID, ErrAlreadyExpired)
	}

	var onExpire *Transition
	if node, ok := d.Node(tok.Node); ok && node.OnExpire != "" {
		tr, found := findTransition(d.TransitionsFrom(tok.Node), node.OnExpire)
		if !found {
			return nil, fmt.Errorf("workflow: expire case %s: on_expire %q from state %q: %w", c.ID, node.OnExpire, tok.Node, ErrInvalidTransition)
		}
		if err := e.checkEffectDB(d, db); err != nil {
			return nil, fmt.Errorf("workflow: expire case %s: %w", c.ID, err)
		}
		if tr.Guard != "" {
			fn, err := e.lookupGuard(tr.Guard)
			if err != nil {
				return nil, err
			}
			ok, err := fn(ctx, db, run.snapshot(tok, tok.Node))
			if err != nil {
				return nil, fmt.Errorf("workflow: expire case %s: guard %q: %w", c.ID, tr.Guard, err)
			}
			if !ok {
				return nil, fmt.Errorf("workflow: expire case %s: guard %q: %w", c.ID, tr.Guard, ErrGuardRejected)
			}
		}
		onExpire = &tr
	}

	deadline := *tok.DeadlineAt
	detail, err := marshalDetail(ExpiryDetail{DeadlineAt: deadline})
	if err != nil {
		return nil, fmt.Errorf("workflow: expire case %s: %w", c.ID, err)
	}
	run.begin(now, actor)
	tok.ExpiredDeadline = &deadline
	run.touch(tok.ID)
	run.emit(&Event{
		Kind: EventExpired, TokenID: tok.ID, ScopeID: tok.ScopeID,
		FromState: tok.Node, ToState: tok.Node, Detail: detail,
	})
	if onExpire != nil {
		cause, err := marshalDetail(expiredCause{Cause: "expired"})
		if err != nil {
			return nil, fmt.Errorf("workflow: expire case %s: %w", c.ID, err)
		}
		// route emits the hop leaving the node first; it carries the cause.
		hop := len(run.events)
		if err := e.route(ctx, db, run, tok, *onExpire, ""); err != nil {
			return nil, fmt.Errorf("workflow: expire case %s: %w", c.ID, err)
		}
		run.events[hop].Detail = cause
	}

	appended, err := e.persist(ctx, db, run)
	if err != nil {
		return nil, fmt.Errorf("workflow: expire case %s: %w", c.ID, err)
	}
	if onExpire != nil {
		if err := e.dispatch(ctx, db, d, *c, appended); err != nil {
			return nil, fmt.Errorf("workflow: expire case %s: dispatch effects: %w", c.ID, err)
		}
	}
	return &MoveResult{Case: c, Events: appended, Joined: run.joined}, nil
}

// OverdueFilter selects the overdue tokens Engine.Overdue lists.
type OverdueFilter struct {
	Tenant string // empty = any
	// Limit is the page size: <= 0 means DefaultListLimit; clamped to
	// MaxListLimit.
	Limit int
	// Before lists tokens whose deadline is strictly before it; the zero
	// value means Config.Now at call time.
	Before time.Time
}

// Page returns the LIMIT a repository query must use for f (see
// InboxFilter.Page); Overdue has no offset, since expiring a token removes it
// from the next call's result.
func (f OverdueFilter) Page() int {
	limit, _, _ := page(f.Limit, 0)
	return limit
}

// Overdue returns one WorkItem per active token of an open case whose
// deadline is before f.Before and not yet recorded as expired (see Expire),
// oldest deadline first, then token ID, at most f.Page() of them. The
// periodic job that calls Overdue and then Expire for each item belongs to
// the consumer: once expired, a token leaves the list, so the job pages by
// calling Overdue again.
func (e *Engine) Overdue(ctx context.Context, db pgxtx.DBTX, f OverdueFilter) ([]WorkItem, error) {
	if f.Before.IsZero() {
		f.Before = e.now()
	}
	f.Limit = f.Page()
	items, err := e.repo.ListOverdue(ctx, db, f)
	if err != nil {
		return nil, fmt.Errorf("workflow: overdue: %w", err)
	}
	return items, nil
}
