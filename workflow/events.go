package workflow

import (
	"bytes"
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/reqctx"
)

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
