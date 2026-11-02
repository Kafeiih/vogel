package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

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
