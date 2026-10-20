package jobs

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	"github.com/kafeiih/vogel/notification"
)

type SendEmailJobArgs struct {
	Message notification.Message `json:"message"`
}

func (SendEmailJobArgs) Kind() string { return "send_email" }

// Notifier must be set when the worker is registered.
type SendEmailWorker struct {
	river.WorkerDefaults[SendEmailJobArgs]
	Notifier notification.Notifier
}

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
