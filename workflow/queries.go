package workflow

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

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
