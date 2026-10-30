package action

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	"github.com/kafeiih/vogel/workflow"
)

// Worker is the River worker for Args jobs: it runs the Registry's Handler
// for the job's effect.
type Worker struct {
	river.WorkerDefaults[Args]
	registry *Registry
}

// NewWorker creates a Worker that resolves effects in r.
func NewWorker(r *Registry) *Worker {
	return &Worker{registry: r}
}

// Work runs the Handler registered for job.Args.Effect. An effect not in the
// Registry is an error River retries with backoff: during a rolling deploy a
// worker still on the previous release may pick up a job for an effect only
// the new release registers. A Handler error is returned as is, so River
// retries unless it is a river.JobCancel.
func (w *Worker) Work(ctx context.Context, job *river.Job[Args]) error {
	a := job.Args
	h, ok := w.registry.handler(a.Effect)
	if !ok {
		return fmt.Errorf("action: case %s seq %d: %w: %s", a.CaseID, a.Seq, workflow.ErrUnknownEffect, a.Effect)
	}
	run := Run{
		Effect:     a.Effect,
		CaseID:     a.CaseID,
		TokenID:    a.TokenID,
		Seq:        a.Seq,
		Definition: a.Definition,
		Version:    a.Version,
		Domain:     a.Domain,
		ExternalID: a.ExternalID,
		Unit:       a.Unit,
		ActorID:    a.ActorID,
		Part:       a.Part,
	}
	if job.JobRow != nil {
		run.Attempt = job.Attempt
	}
	return h.Handle(context.WithValue(ctx, partsKey{}, w.enqueueParts(a)), run)
}

type partsKey struct{}

// enqueuePartsFunc enqueues one job per item 1..n of the effect a Handler is
// running, each unique, so a retried effect job does not enqueue them twice.
type enqueuePartsFunc func(ctx context.Context, n int) error

func (w *Worker) enqueueParts(a Args) enqueuePartsFunc {
	return func(ctx context.Context, n int) error {
		for i := 1; i <= n; i++ {
			part := a
			part.Part = i
			if _, err := w.registry.queue.Enqueue(ctx, part, insertOpts()); err != nil {
				return fmt.Errorf("action: case %s seq %d: enqueue %q part %d: %w", a.CaseID, a.Seq, a.Effect, i, err)
			}
		}
		return nil
	}
}

// AddWorker registers w on workers, like river.AddWorkerSafely: it fails
// when a worker for Kind is already registered.
func AddWorker(workers *river.Workers, w *Worker) error {
	return river.AddWorkerSafely[Args](workers, w)
}
