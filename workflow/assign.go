package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

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
