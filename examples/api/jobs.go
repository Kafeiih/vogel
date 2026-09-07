package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/notification"
)

// NotifyReviewersArgs is the payload for the notify_reviewers background
// job, enqueued by DocumentHandler.Create in the SAME database transaction
// as the document row, its workflow case, and its audit entry -- see
// worker.Queue.EnqueueTx and the doc comment on DocumentHandler.Create.
type NotifyReviewersArgs struct {
	DocumentID string `json:"document_id"`
	Title      string `json:"title"`
}

// Kind implements river.JobArgs.
func (NotifyReviewersArgs) Kind() string { return "notify_reviewers" }

// NotifyReviewersWorker sends a notification when a document enters review.
// It depends only on the notification.Notifier and audit.Repository-backed
// audit.Recorder ports -- never on a concrete adapter such as
// notification/smtp or notification/sendgrid -- so swapping fakeNotifier for
// a real one in main.go's composition root changes nothing here.
type NotifyReviewersWorker struct {
	river.WorkerDefaults[NotifyReviewersArgs]

	notifier notification.Notifier
	recorder *audit.Recorder
	pool     *pgxpool.Pool
	from     string
	logger   *slog.Logger
}

// Work implements river.Worker.
func (w *NotifyReviewersWorker) Work(ctx context.Context, job *river.Job[NotifyReviewersArgs]) error {
	msg := &notification.Message{
		To:      "reviewers@example.com",
		From:    w.from,
		Subject: fmt.Sprintf("Document ready for review: %s", job.Args.Title),
		Text:    fmt.Sprintf("Document %s (%s) is ready for review.", job.Args.Title, job.Args.DocumentID),
	}
	if err := msg.Validate(); err != nil {
		return fmt.Errorf("notify reviewers: invalid message: %w", err)
	}
	if err := w.notifier.Send(ctx, msg); err != nil {
		return fmt.Errorf("notify reviewers: send: %w", err)
	}

	w.logger.InfoContext(ctx, "reviewers notified", "document_id", job.Args.DocumentID)

	// audit.Recorder.Record takes Source as a required positional argument,
	// not an Option, precisely so a call site like this one cannot silently
	// produce an HTTP-shaped audit row for work that never touched a
	// request: audit.SourceWorker here is a compile-time-enforced statement
	// of this entry's true origin, distinct from audit.SourceHTTP used by
	// DocumentHandler. Inside a worker there is no inbound *http.Request and
	// therefore no auth.FromContext principal, so Record's actor fields
	// (ActorID, Username) come out empty here by design -- that reflects
	// reality for a system-initiated notification, not a bug to fix.
	if err := w.recorder.Record(ctx, w.pool, audit.SourceWorker, audit.ActionExecute,
		"document", job.Args.DocumentID,
		audit.WithOperationName("notify_reviewers"),
	); err != nil {
		return fmt.Errorf("notify reviewers: record audit entry: %w", err)
	}

	return nil
}

// RegisterWorkers builds the river.Workers registry this example's queue
// runs. Passed as NewRiverQueue's second argument; passing nil instead
// switches a queue to insert-only mode (see the comment in main.go on why
// this example's single process passes a non-nil registry here).
func RegisterWorkers(w *NotifyReviewersWorker) *river.Workers {
	workers := river.NewWorkers()
	river.AddWorker(workers, w)
	return workers
}
