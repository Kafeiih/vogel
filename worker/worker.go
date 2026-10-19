// Package worker provides background job processing for vogel-based
// applications, built on River (github.com/riverqueue/river) backed by
// PostgreSQL.
//
// The Queue interface abstracts the job queue for the application layer, and
// RiverQueue (see river.go) is the River-backed implementation this package
// ships. Both live in the same package on purpose: a consumer defining River
// jobs already imports river directly (river.WorkerDefaults[T],
// river.Job[T]), so hiding River behind a separate port would be ceremony
// without benefit. This package depends on River openly, and that's fine.
//
// Usage from the application layer:
//
//	// Enqueue a job within the same transaction as a business mutation:
//	txManager.WithTx(ctx, func(txCtx context.Context) error {
//	    repo.Create(txCtx, entity)
//	    tx := repository.TxFromContext(txCtx)
//	    _, err := queue.EnqueueTx(txCtx, tx, MyJobArgs{ID: entity.ID}, nil)
//	    return err
//	})
//
//	// Enqueue a job without a transaction:
//	queue.Enqueue(ctx, MyJobArgs{ID: "123"}, nil)
package worker

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Queue abstracts the job queue system for background processing.
//
// Two modes of operation:
//   - Insert-only: the API process calls Enqueue/EnqueueTx but never Start.
//   - Full worker: the worker binary calls Start to begin processing jobs.
type Queue interface {
	// Enqueue adds a job to the queue outside of a transaction.
	Enqueue(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)

	// EnqueueTx adds a job within an existing database transaction. The job
	// is only visible after the transaction commits (Transactional Outbox).
	EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)

	// Start begins processing jobs. Blocks until Stop is called or the
	// context is canceled. Only the worker binary should call Start — the
	// API process never does.
	Start(ctx context.Context) error

	// Stop initiates graceful shutdown. Running jobs are allowed to
	// complete.
	Stop(ctx context.Context) error
}

// Config configures a RiverQueue.
//
// Applying defaults is the caller's responsibility: this package does not
// read environment variables or fill in zero values itself. Both
// applications this package was extracted from (go-crucible, go-licencias)
// parse an equivalent config with Schema defaulting to "river" and
// DefaultMaxWorkers defaulting to 100 — mirror those defaults in your own
// application config if you want the same behavior.
type Config struct {
	// Schema is the PostgreSQL schema River's tables live in, and that
	// Migrate creates tables inside of. Must be a valid, unquoted PostgreSQL
	// identifier (see isValidSchemaName in schema.go). Typical default:
	// "river".
	Schema string

	// DefaultMaxWorkers is the max concurrent workers for the default River
	// queue, used when Queues is empty. Typical default: 100.
	DefaultMaxWorkers int

	// Queues configures named queues and their max concurrent workers. If
	// empty, a single default queue (river.QueueDefault) is used with
	// DefaultMaxWorkers.
	Queues map[string]int

	// PollOnly makes a worker-mode client find new jobs by polling instead
	// of waiting on PostgreSQL LISTEN/NOTIFY; it is passed to River's
	// river.Config.PollOnly. LISTEN needs a session that stays on one server
	// connection, so set it when the worker connects through PgBouncer in
	// transaction pooling mode. The cost is latency: a new job may wait up
	// to River's FetchPollInterval (1s by default) before it is locked for
	// work, and a new leader takes up to five seconds to elect itself after
	// the previous one resigns. Ignored in insert-only mode, which never
	// listens. Default false: LISTEN/NOTIFY, as before.
	PollOnly bool
}

// Option configures optional NewRiverQueue behavior.
type Option func(*options)

// options collects the values set by Option funcs passed to NewRiverQueue.
type options struct {
	periodicJobs []*river.PeriodicJob

	// metrics is set by WithMetrics; metricsRegisterer is where it
	// registers, nil meaning prometheus.DefaultRegisterer.
	metrics           bool
	metricsRegisterer prometheus.Registerer
}

// newOptions applies opts in order and returns the resulting options.
func newOptions(opts ...Option) *options {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// WithPeriodicJobs registers River periodic jobs on the RiverQueue created by
// NewRiverQueue.
//
// Periodic jobs are only ever scheduled in worker-binary mode (workers !=
// nil passed to NewRiverQueue): an insert-only client processes nothing, so
// periodic jobs passed alongside a nil *river.Workers are silently not
// registered. Omitting this option entirely — the go-licencias call shape —
// is equivalent to passing WithPeriodicJobs(nil): no periodic jobs are
// registered, same as before this option existed.
func WithPeriodicJobs(jobs []*river.PeriodicJob) Option {
	return func(o *options) {
		o.periodicJobs = jobs
	}
}

// WithMetrics records the duration of every job attempt the RiverQueue works
// as the Prometheus histogram river_job_duration_seconds{kind,outcome},
// registered on reg (prometheus.DefaultRegisterer if nil). outcome is
// success, error, cancel (river.JobCancel), snooze (river.JobSnooze) or
// panic; kind is the job's Kind(). The histogram's _count doubles as the
// attempt counter by outcome. Registering twice on the same Registerer
// reuses the collector.
//
// The metrics are a worker middleware, installed after the request ID one, so
// they only exist in worker-binary mode: with a nil *river.Workers the option
// is ignored and nothing is registered, as with WithPeriodicJobs. Without
// this option no metrics middleware is installed.
func WithMetrics(reg prometheus.Registerer) Option {
	return func(o *options) {
		o.metrics = true
		o.metricsRegisterer = reg
	}
}

// buildMetrics returns the metrics middleware WithMetrics asked for, or nil
// when it was not passed or the client is insert-only (workers == nil).
func (o *options) buildMetrics(workers *river.Workers) (*metricsMiddleware, error) {
	if !o.metrics || workers == nil {
		return nil, nil
	}
	reg := o.metricsRegisterer
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	mw, err := newMetricsMiddleware(reg)
	if err != nil {
		return nil, fmt.Errorf("worker: registering job metrics: %w", err)
	}
	return mw, nil
}
