// Package jobs contains concrete job handlers for background processing.
//
// Each job defines its Args struct (what data it carries) and a Worker (how to process it).
// Workers are registered with River in cmd/worker/main.go.
//
// To add a new job type:
//  1. Create a new file (e.g., send_email.go)
//  2. Define XxxArgs with Kind() and JSON tags
//  3. Define XxxWorker with Work() method
//  4. Register in cmd/worker/main.go: river.AddWorker(workers, &XxxWorker{})
package jobs

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"
)

// ExampleJobArgs carries the payload for an example job.
// This serves as a reference implementation for new job types.
type ExampleJobArgs struct {
	Message string `json:"message"`
}

// Kind returns the unique identifier for this job type.
// River uses this to route jobs to the correct worker.
func (ExampleJobArgs) Kind() string { return "example" }

// ExampleWorker processes ExampleJobArgs.
type ExampleWorker struct {
	river.WorkerDefaults[ExampleJobArgs]
}

// Work processes a single example job.
func (w *ExampleWorker) Work(ctx context.Context, job *river.Job[ExampleJobArgs]) error {
	slog.InfoContext(ctx, "example job processed",
		"message", job.Args.Message,
	)
	return nil
}
