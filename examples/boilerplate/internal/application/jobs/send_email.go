package jobs

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	"github.com/kafeiih/vogel/notification"
)

// SendEmailJobArgs carries the email message as a River job payload.
//
// Enqueue example:
//
//	queue.EnqueueTx(ctx, tx, jobs.SendEmailJobArgs{
//	    Message: notification.Message{
//	        To: "user@example.com", From: "noreply@inst.cl",
//	        Subject: "Welcome", HTML: "<h1>Hello</h1>",
//	    },
//	}, nil)
type SendEmailJobArgs struct {
	Message notification.Message `json:"message"`
}

// Kind returns the unique identifier for email jobs.
func (SendEmailJobArgs) Kind() string { return "send_email" }

// SendEmailWorker processes email delivery jobs.
// The Notifier field must be set during worker registration.
type SendEmailWorker struct {
	river.WorkerDefaults[SendEmailJobArgs]
	Notifier notification.Notifier
}

// Work sends the email via the configured Notifier.
func (w *SendEmailWorker) Work(ctx context.Context, job *river.Job[SendEmailJobArgs]) error {
	msg := &job.Args.Message

	if err := msg.Validate(); err != nil {
		return fmt.Errorf("send_email: %w", err)
	}

	if err := w.Notifier.Send(ctx, msg); err != nil {
		return fmt.Errorf("send_email: %w", err)
	}

	return nil
}
