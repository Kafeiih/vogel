package worker

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
)

// RiverQueue implements Queue using River over PostgreSQL. River uses
// PostgreSQL as its job store via SELECT FOR UPDATE SKIP LOCKED, providing
// transactional job enqueue (Outbox Pattern) with zero extra infrastructure.
type RiverQueue struct {
	client *river.Client[pgx.Tx]
	pool   *pgxpool.Pool
	config Config
	logger *slog.Logger
}

// RiverQueue must satisfy Queue. The interface exists so consumers can fake
// the queue in their own tests; without this assertion a signature drifting
// apart from Queue would only surface in a consumer, not here.
var _ Queue = (*RiverQueue)(nil)

// NewRiverQueue creates a River-backed queue.
//
// If workers is nil, the client operates in insert-only mode (API process).
// If workers is provided, the client can be started to process jobs (worker
// binary). Pass WithPeriodicJobs to register periodic jobs — see its doc
// comment for how that interacts with insert-only mode.
func NewRiverQueue(pool *pgxpool.Pool, workers *river.Workers, cfg Config, logger *slog.Logger, opts ...Option) (*RiverQueue, error) {
	if pool == nil {
		return nil, fmt.Errorf("worker: pgxpool.Pool is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("worker: logger is required")
	}

	o := newOptions(opts...)
	riverCfg := buildRiverConfig(cfg, workers, o.periodicJobs)

	client, err := river.NewClient(riverpgxv5.New(pool), riverCfg)
	if err != nil {
		return nil, fmt.Errorf("worker: creating river client: %w", err)
	}

	return &RiverQueue{
		client: client,
		pool:   pool,
		config: cfg,
		logger: logger,
	}, nil
}

// Migrate runs River's schema migrations. Call this before Start in the
// worker binary. The target schema must already exist — see EnsureSchema.
func (q *RiverQueue) Migrate(ctx context.Context) error {
	migrator, err := rivermigrate.New(riverpgxv5.New(q.pool), &rivermigrate.Config{
		Schema: q.config.Schema,
	})
	if err != nil {
		return fmt.Errorf("worker: creating migrator: %w", err)
	}

	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("worker: running migrations: %w", err)
	}

	for _, v := range res.Versions {
		q.logger.Info("river migration applied", "version", v.Version)
	}

	return nil
}

// Enqueue adds a job to the queue outside of a transaction.
func (q *RiverQueue) Enqueue(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return q.client.Insert(ctx, args, opts)
}

// EnqueueTx adds a job within an existing database transaction. The job is
// only visible after the transaction commits.
func (q *RiverQueue) EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return q.client.InsertTx(ctx, tx, args, opts)
}

// Start begins processing jobs. Blocks by waiting on the River client.
func (q *RiverQueue) Start(ctx context.Context) error {
	q.logger.Info("starting river worker",
		"schema", q.config.Schema,
		"queues", len(q.config.Queues),
	)
	return q.client.Start(ctx)
}

// Stop initiates graceful shutdown.
func (q *RiverQueue) Stop(ctx context.Context) error {
	q.logger.Info("stopping river worker")
	return q.client.Stop(ctx)
}

// buildRiverConfig assembles the river.Config used by NewRiverQueue.
// Schema MUST be set: it tells the River client where to look for the tables
// the migrator created. Without it the client searches the default
// search_path (public) and Start fails with `relation "river_queue" does not
// exist`.
//
// Workers, Queues, and periodicJobs are only relevant in worker-binary mode.
// The API process creates the client in insert-only mode (workers == nil) to
// enqueue jobs without consuming them, so none of the three are set then —
// periodicJobs included, even if the caller passed some in: an insert-only
// client processes nothing, so there is nothing to run them.
func buildRiverConfig(cfg Config, workers *river.Workers, periodicJobs []*river.PeriodicJob) *river.Config {
	rc := &river.Config{
		Schema: cfg.Schema,
	}
	if workers != nil {
		rc.Workers = workers
		rc.Queues = buildQueueConfig(cfg)
		if len(periodicJobs) > 0 {
			rc.PeriodicJobs = periodicJobs
		}
	}
	return rc
}

// buildQueueConfig converts Config.Queues to River's QueueConfig map. If no
// queues are configured, uses the default queue with DefaultMaxWorkers.
func buildQueueConfig(cfg Config) map[string]river.QueueConfig {
	if len(cfg.Queues) == 0 {
		return map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: cfg.DefaultMaxWorkers},
		}
	}

	queues := make(map[string]river.QueueConfig, len(cfg.Queues))
	for name, maxWorkers := range cfg.Queues {
		queues[name] = river.QueueConfig{MaxWorkers: maxWorkers}
	}
	return queues
}
