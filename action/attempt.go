package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"
)

// Outcome is how one attempt of an effect job ended, as the attempt log
// records it.
type Outcome string

const (
	// OutcomeSucceeded: the Handler returned nil.
	OutcomeSucceeded Outcome = "succeeded"
	// OutcomeRetry: the Handler failed (or panicked) and River will retry.
	OutcomeRetry Outcome = "retry"
	// OutcomeCancelled: the Handler returned river.JobCancel.
	OutcomeCancelled Outcome = "cancelled"
	// OutcomeSnoozed: the Handler returned river.JobSnooze.
	OutcomeSnoozed Outcome = "snoozed"
	// OutcomeDiscarded: the Handler failed on River's last attempt.
	OutcomeDiscarded Outcome = "discarded"
	// OutcomeUnknownEffect: no Handler is registered for the effect; River
	// retries the job (see Worker.Work).
	OutcomeUnknownEffect Outcome = "unknown_effect"
)

// Attempt is one row of the attempt log: one run of an effect job.
type Attempt struct {
	ID     uuid.UUID
	CaseID uuid.UUID
	Seq    int64
	Effect string
	Part   int
	// Attempt is River's attempt number, starting at 1. River gives a
	// snoozed job its attempt back, so the run after a snooze has the same
	// Attempt and a higher Snooze.
	Attempt int
	// Snooze is how many times the job had been snoozed when the attempt
	// started.
	Snooze     int
	JobID      int64
	StartedAt  time.Time
	FinishedAt time.Time
	Outcome    Outcome
	// ErrorClass is the Go type of the innermost error and, in parentheses,
	// the well-known sentinel it matched (context.Canceled,
	// context.DeadlineExceeded, workflow.ErrUnknownEffect); "panic" for a
	// Handler that panicked; empty on success. Never the error message,
	// which can carry personal data.
	ErrorClass string
	RequestID  string
	TraceID    string
	ActorKind  workflow.ActorKind
	ActorID    string
	// ResultRef and ResultDigest are what the Handler passed to
	// RecordResult, empty when it did not.
	ResultRef    string
	ResultDigest string
}

// AttemptRepository reads the attempt log. action/postgres implements it.
// Rows are append-only: there is no update or delete.
type AttemptRepository interface {
	// ListByCase returns every attempt of the case, ordered by event seq,
	// then attempt number.
	ListByCase(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Attempt, error)
	// ListByEvent returns the attempts of the effects one event triggered,
	// in the same order.
	ListByEvent(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, seq int64) ([]Attempt, error)
}

// insertAttemptSQL writes one attempt row. (job_id, attempt, snooze) is
// unique, so writing the same attempt twice (a worker that crashed after the
// insert and ran it again) is a no-op.
const insertAttemptSQL = `
    INSERT INTO action_attempt (id, case_id, seq, effect, part, attempt, snooze, job_id,
        started_at, finished_at, outcome, error_class, request_id, trace_id,
        actor_kind, actor_id, result_ref, result_digest)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
    ON CONFLICT (job_id, attempt, snooze) DO NOTHING`

func insertAttempt(ctx context.Context, db pgxtx.DBTX, a *Attempt) error {
	_, err := db.Exec(ctx, insertAttemptSQL,
		a.ID, a.CaseID, a.Seq, a.Effect, a.Part, a.Attempt, a.Snooze, a.JobID,
		a.StartedAt, a.FinishedAt, string(a.Outcome), a.ErrorClass, a.RequestID, a.TraceID,
		string(a.ActorKind), a.ActorID, a.ResultRef, a.ResultDigest,
	)
	return err
}

// attemptOutcome classifies what a Handler returned the way River's executor
// does: snooze and cancel are found with errors.As, and any other error on
// the job's last attempt discards it.
func attemptOutcome(err error, job *river.Job[Args]) Outcome {
	if err == nil {
		return OutcomeSucceeded
	}
	var snooze *rivertype.JobSnoozeError
	if errors.As(err, &snooze) {
		return OutcomeSnoozed
	}
	if IsCancel(err) {
		return OutcomeCancelled
	}
	if job.JobRow != nil && job.MaxAttempts > 0 && job.Attempt >= job.MaxAttempts {
		return OutcomeDiscarded
	}
	return OutcomeRetry
}

// errorClassPanic is the ErrorClass of a Handler that panicked.
const errorClassPanic = "panic"

// knownSentinels are the sentinels errorClass names. The list is closed, so
// the class stays a bounded, message-free label.
var knownSentinels = []struct {
	err  error
	name string
}{
	{context.Canceled, "context.Canceled"},
	{context.DeadlineExceeded, "context.DeadlineExceeded"},
	{workflow.ErrUnknownEffect, "workflow.ErrUnknownEffect"},
}

// errorClass describes err without its message: the Go type of the innermost
// error, plus the known sentinel it matched. A river.JobCancel or JobSnooze
// wrapper is looked through; the outcome already says which it was.
func errorClass(err error) string {
	if err == nil {
		return ""
	}
	inner := err
	for next := errors.Unwrap(inner); next != nil; next = errors.Unwrap(next) {
		inner = next
	}
	class := fmt.Sprintf("%T", inner)
	for _, s := range knownSentinels {
		if errors.Is(err, s.err) {
			return class + " (" + s.name + ")"
		}
	}
	return class
}

// snoozeCount returns the job's snooze count from River's "snoozes" metadata
// key, zero when it has none.
func snoozeCount(metadata []byte) int {
	if len(metadata) == 0 {
		return 0
	}
	var m struct {
		Snoozes int `json:"snoozes"`
	}
	if err := json.Unmarshal(metadata, &m); err != nil {
		return 0
	}
	return m.Snoozes
}
