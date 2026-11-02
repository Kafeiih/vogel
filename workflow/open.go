package workflow

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

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
	// The root scope and the root token share the case ID, so a case that
	// never splits needs no other identifier.
	root := &Token{
		ID: c.ID, CaseID: c.ID, ScopeID: c.ID, Node: start.ID, Status: TokenActive,
		Eligible: resolvedEligibility(start, c.Unit), EnteredSeq: 1, EnteredAt: now,
	}
	deadline, err := e.deadlineFor(ctx, start, now, *c, *root)
	if err != nil {
		return nil, fmt.Errorf("workflow: open case: %w", err)
	}
	c.DeadlineAt, root.DeadlineAt = deadline, deadline

	if err := e.repo.Create(ctx, db, c); err != nil {
		return nil, fmt.Errorf("workflow: open case: %w", err)
	}
	if err := e.repo.CreateScope(ctx, db, &Scope{
		ID: c.ID, CaseID: c.ID, Expected: 1, Status: ScopeOpen, OpenedSeq: 1,
	}); err != nil {
		return nil, fmt.Errorf("workflow: open case: create root scope: %w", err)
	}
	if err := e.repo.CreateToken(ctx, db, root); err != nil {
		return nil, fmt.Errorf("workflow: open case: create root token: %w", err)
	}

	evt, err := e.appendEvent(ctx, db, c, &Event{
		Seq:        c.LastSeq,
		Kind:       EventOpened,
		TokenID:    c.ID,
		ScopeID:    c.ID,
		ToState:    start.ID,
		DeadlineAt: copyTime(deadline),
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
