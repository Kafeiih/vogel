package jobs

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"
)

type ExampleJobArgs struct {
	Message string `json:"message"`
}

func (ExampleJobArgs) Kind() string { return "example" }

type ExampleWorker struct {
	river.WorkerDefaults[ExampleJobArgs]
}

func (w *ExampleWorker) Work(ctx context.Context, job *river.Job[ExampleJobArgs]) error {
	slog.InfoContext(ctx, "example job processed",
		"message", job.Args.Message,
	)
	return nil
}
