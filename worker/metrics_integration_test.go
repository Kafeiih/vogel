//go:build integration

package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/worker"
)

// signalArgs is a job whose worker reports its Tag once it ran.
type signalArgs struct {
	Tag string `json:"tag"`
}

func (signalArgs) Kind() string { return "signal" }

type signalWorker struct {
	river.WorkerDefaults[signalArgs]
	done chan string
}

func (w *signalWorker) Work(_ context.Context, job *river.Job[signalArgs]) error {
	w.done <- job.Args.Tag
	return nil
}

// startConsumer builds a worker-mode queue for w on a migrated schema, starts
// it, and stops it when the test ends.
func startConsumer(t *testing.T, pool *pgxpool.Pool, cfg worker.Config, w *signalWorker, opts ...worker.Option) {
	t.Helper()
	workers := river.NewWorkers()
	river.AddWorker(workers, w)
	consumer, err := worker.NewRiverQueue(pool, workers, cfg, silentLogger(), opts...)
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, consumer.Start(runCtx))
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = consumer.Stop(stopCtx)
	})
}

// migratedQueue ensures and migrates schema and returns an insert-only queue
// to enqueue with.
func migratedQueue(t *testing.T, pool *pgxpool.Pool, cfg worker.Config) *worker.RiverQueue {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, worker.EnsureSchema(ctx, pool, cfg.Schema))
	api, err := worker.NewRiverQueue(pool, nil, cfg, silentLogger())
	require.NoError(t, err)
	require.NoError(t, api.Migrate(ctx))
	return api
}

func waitForJob(t *testing.T, done <-chan string, want string) {
	t.Helper()
	select {
	case got := <-done:
		assert.Equal(t, want, got)
	case <-time.After(30 * time.Second):
		t.Fatalf("job %q not worked in time", want)
	}
}

// successCount returns the sample count of
// river_job_duration_seconds{kind,outcome="success"} on reg.
func successCount(t *testing.T, reg *prometheus.Registry, kind string) uint64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		if mf.GetName() != "river_job_duration_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["kind"] == kind && labels["outcome"] == "success" {
				return m.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

// A job worked by a queue built with WithMetrics is observed in
// river_job_duration_seconds with its kind and outcome success.
func TestWithMetrics_WorkedJobIsObserved(t *testing.T) {
	pool := startTestPool(t)
	cfg := worker.Config{Schema: "river", DefaultMaxWorkers: 2}
	api := migratedQueue(t, pool, cfg)

	reg := prometheus.NewRegistry()
	w := &signalWorker{done: make(chan string, 1)}
	startConsumer(t, pool, cfg, w, worker.WithMetrics(reg))

	_, err := api.Enqueue(context.Background(), signalArgs{Tag: "metered"}, nil)
	require.NoError(t, err)
	waitForJob(t, w.done, "metered")

	// The observation is made when Work returns, just after the worker
	// signalled, so poll for it.
	require.Eventually(t, func() bool {
		return successCount(t, reg, "signal") == 1
	}, 10*time.Second, 50*time.Millisecond)
}

// PollOnly drops LISTEN/NOTIFY, so the worker must find a job enqueued after
// it started by polling alone.
func TestPollOnly_WorkerStillProcessesJobs(t *testing.T) {
	pool := startTestPool(t)
	cfg := worker.Config{Schema: "river", DefaultMaxWorkers: 2, PollOnly: true}
	api := migratedQueue(t, pool, cfg)

	w := &signalWorker{done: make(chan string, 1)}
	startConsumer(t, pool, cfg, w)

	_, err := api.Enqueue(context.Background(), signalArgs{Tag: "polled"}, nil)
	require.NoError(t, err)
	waitForJob(t, w.done, "polled")

	// No session of this database ever issued LISTEN: the worker did not
	// rely on a session-pinned connection, which is what makes it usable
	// behind PgBouncer in transaction mode.
	var listening int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND query ILIKE 'listen%'`,
	).Scan(&listening))
	assert.Zero(t, listening, "a PollOnly worker must not LISTEN")
}
