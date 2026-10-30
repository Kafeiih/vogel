package action

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/reqctx"
	"github.com/kafeiih/vogel/workflow"
)

// attemptLogErrorsMetricName counts attempt rows the Worker failed to write.
const attemptLogErrorsMetricName = "vogel_action_attempt_log_errors_total"

// attemptLogTimeout bounds the attempt-log insert. It runs on a context
// detached from the job's, so a job cancelled by its timeout still gets its
// row, but a stuck database cannot hold the worker slot forever.
const attemptLogTimeout = 5 * time.Second

// Worker is the River worker for Args jobs: it runs the Registry's Handler
// for the job's effect.
type Worker struct {
	river.WorkerDefaults[Args]
	registry *Registry

	attemptDB  pgxtx.DBTX
	logger     *slog.Logger
	registerer prometheus.Registerer
	traceIDs   func(ctx context.Context) (traceID, spanID string)
	logErrors  prometheus.Counter
	now        func() time.Time
}

// WorkerOption configures a Worker.
type WorkerOption func(*Worker)

// WithAttemptLog makes the Worker write one action_attempt row per attempt
// (action/migrations) on db, normally the pool. The row is written after the
// Handler returns, in its own statement, never inside the Handler's work. A
// failed write never fails or retries the job: it is logged and counted by
// vogel_action_attempt_log_errors_total. Without this option nothing is
// written.
func WithAttemptLog(db pgxtx.DBTX) WorkerOption {
	return func(w *Worker) { w.attemptDB = db }
}

// WithLogger sets the logger attempt-log failures are reported on. The
// default is slog.Default().
func WithLogger(l *slog.Logger) WorkerOption {
	return func(w *Worker) {
		if l != nil {
			w.logger = l
		}
	}
}

// WithMetricsRegisterer sets where vogel_action_attempt_log_errors_total is
// registered when the attempt log is on. The default is
// prometheus.DefaultRegisterer.
func WithMetricsRegisterer(reg prometheus.Registerer) WorkerOption {
	return func(w *Worker) {
		if reg != nil {
			w.registerer = reg
		}
	}
}

// WithTraceIDs sets how the attempt log reads the trace ID of the job's span
// (otelriver starts one per job). It has the shape of workflow.Config.TraceIDs,
// so otel.SampledTraceIDs fits. Without it trace_id is left empty.
func WithTraceIDs(fn func(ctx context.Context) (traceID, spanID string)) WorkerOption {
	return func(w *Worker) { w.traceIDs = fn }
}

// NewWorker creates a Worker that resolves effects in r.
func NewWorker(r *Registry, opts ...WorkerOption) *Worker {
	w := &Worker{
		registry:   r,
		logger:     slog.Default(),
		registerer: prometheus.DefaultRegisterer,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(w)
	}
	if w.attemptDB != nil {
		w.logErrors = w.attemptLogErrorsCounter()
	}
	return w
}

// attemptLogErrorsCounter registers the attempt-log error counter, reusing an
// already registered one as worker's metrics middleware does, so two Workers
// on one Registerer share it. A registration that fails otherwise leaves the
// counter unregistered: the attempt log must not stop the worker binary.
func (w *Worker) attemptLogErrorsCounter() prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Name: attemptLogErrorsMetricName,
		Help: "Attempt-log rows (action_attempt) the action worker failed to write.",
	})
	if err := w.registerer.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(prometheus.Counter); ok {
				return existing
			}
		}
		w.logger.Warn("action: attempt-log error counter not registered", "metric", attemptLogErrorsMetricName, "error", err)
	}
	return c
}

// Work runs the Handler registered for job.Args.Effect. An effect not in the
// Registry is an error River retries with backoff: during a rolling deploy a
// worker still on the previous release may pick up a job for an effect only
// the new release registers. A Handler error is returned as is, so River
// retries unless it is a river.JobCancel. With WithAttemptLog, every call
// (an unknown effect and a Handler panic included) writes one attempt row.
func (w *Worker) Work(ctx context.Context, job *river.Job[Args]) error {
	a := job.Args
	run := Run{
		Effect:     a.Effect,
		CaseID:     a.CaseID,
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
	started := w.now()

	h, ok := w.registry.handler(a.Effect)
	if !ok {
		err := fmt.Errorf("action: case %s seq %d: %w: %s", a.CaseID, a.Seq, workflow.ErrUnknownEffect, a.Effect)
		w.recordAttempt(ctx, job, run, started, OutcomeUnknownEffect, errorClass(err))
		return err
	}
	ctx = context.WithValue(ctx, partsKey{}, w.enqueueParts(a))
	if w.attemptDB == nil {
		return h.Handle(ctx, run)
	}

	panicked := true
	defer func() {
		if panicked {
			w.recordAttempt(ctx, job, run, started, attemptOutcome(errors.New(errorClassPanic), job), errorClassPanic)
		}
	}()
	err := h.Handle(ctx, run)
	panicked = false
	w.recordAttempt(ctx, job, run, started, attemptOutcome(err, job), errorClass(err))
	return err
}

// recordAttempt writes the attempt row when the attempt log is on. A job
// without a JobRow (a Worker called directly, outside River) has no job ID
// or attempt to key a row on and writes nothing.
func (w *Worker) recordAttempt(ctx context.Context, job *river.Job[Args], run Run, started time.Time, outcome Outcome, class string) {
	if w.attemptDB == nil || job.JobRow == nil {
		return
	}
	at := &Attempt{
		CaseID:     run.CaseID,
		Seq:        run.Seq,
		Effect:     run.Effect,
		Part:       run.Part,
		Attempt:    job.Attempt,
		Snooze:     snoozeCount(job.Metadata),
		JobID:      job.ID,
		StartedAt:  started,
		FinishedAt: w.now(),
		Outcome:    outcome,
		ErrorClass: class,
		RequestID:  reqctx.RequestIDFromContext(ctx),
		ActorID:    run.ActorID,
	}
	if w.traceIDs != nil {
		at.TraceID, _ = w.traceIDs(ctx)
	}
	id, err := uuid.NewV7()
	if err == nil {
		at.ID = id
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attemptLogTimeout)
		defer cancel()
		err = insertAttempt(ctx, w.attemptDB, at)
	}
	if err != nil {
		w.logErrors.Inc()
		w.logger.ErrorContext(ctx, "action: write attempt log",
			"case_id", at.CaseID, "seq", at.Seq, "effect", at.Effect, "part", at.Part,
			"attempt", at.Attempt, "job_id", at.JobID, "outcome", string(at.Outcome), "error", err)
	}
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
