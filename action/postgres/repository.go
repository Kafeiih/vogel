// Package postgres provides the PostgreSQL-backed implementation of the
// action.AttemptRepository port, reading the action_attempt table
// (vogel/action/migrations) with native pgx.
package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"
)

const attemptColumns = `id, case_id, seq, effect, part, attempt, snooze, job_id,
    started_at, finished_at, outcome, error_class, request_id, trace_id,
    actor_kind, actor_id, result_ref, result_digest`

// attemptOrder sorts a case's attempts for a timeline: by event, then by
// attempt number, then by when they started (the run after a snooze shares
// its attempt number), with the effect, part and id as tie-breakers.
const attemptOrder = `ORDER BY seq, attempt, started_at, effect, part, id`

// AttemptRepository implements action.AttemptRepository using pgx. It is
// stateless: every method receives the pgxtx.DBTX to run against.
type AttemptRepository struct{}

var _ action.AttemptRepository = (*AttemptRepository)(nil)

// NewAttemptRepository creates a stateless PostgreSQL-backed attempt
// repository.
func NewAttemptRepository() *AttemptRepository {
	return &AttemptRepository{}
}

// ListByCase returns every attempt of caseID, ordered by seq, then attempt.
func (r *AttemptRepository) ListByCase(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]action.Attempt, error) {
	rows, err := db.Query(ctx, `SELECT `+attemptColumns+` FROM action_attempt WHERE case_id = $1 `+attemptOrder, caseID)
	if err != nil {
		return nil, fmt.Errorf("action: list attempts by case: %w", err)
	}
	return collectAttempts(rows, "list attempts by case")
}

// ListByEvent returns the attempts of the effects the event at seq of caseID
// triggered, in the ListByCase order.
func (r *AttemptRepository) ListByEvent(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, seq int64) ([]action.Attempt, error) {
	rows, err := db.Query(ctx, `SELECT `+attemptColumns+` FROM action_attempt WHERE case_id = $1 AND seq = $2 `+attemptOrder, caseID, seq)
	if err != nil {
		return nil, fmt.Errorf("action: list attempts by event: %w", err)
	}
	return collectAttempts(rows, "list attempts by event")
}

func collectAttempts(rows pgx.Rows, op string) ([]action.Attempt, error) {
	attempts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (action.Attempt, error) {
		var (
			a         action.Attempt
			outcome   string
			actorKind string
		)
		err := row.Scan(&a.ID, &a.CaseID, &a.Seq, &a.Effect, &a.Part, &a.Attempt, &a.Snooze, &a.JobID,
			&a.StartedAt, &a.FinishedAt, &outcome, &a.ErrorClass, &a.RequestID, &a.TraceID,
			&actorKind, &a.ActorID, &a.ResultRef, &a.ResultDigest)
		a.Outcome = action.Outcome(outcome)
		a.ActorKind = workflow.ActorKind(actorKind)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("action: %s: %w", op, err)
	}
	return attempts, nil
}
