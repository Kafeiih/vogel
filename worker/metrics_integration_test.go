//go:build integration

package worker_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

// statementRecorder is a pgx.QueryTracer that keeps every SQL statement the
// pool's connections run, so a test can assert on what was ever sent, not
// only on the last statement pg_stat_activity shows per session.
type statementRecorder struct {
	mu   sync.Mutex
	sqls []string
}

func (r *statementRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	r.sqls = append(r.sqls, data.SQL)
	r.mu.Unlock()
	return ctx
}

func (r *statementRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// listens reports whether any recorded statement was a LISTEN.
func (r *statementRecorder) listens() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, sql := range r.sqls {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "LISTEN") {
			return true
		}
	}
	return false
}

// startTracedPool opens a pool on its own container whose every statement is
// recorded.
func startTracedPool(t *testing.T) (*pgxpool.Pool, *statementRecorder) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(startTestPostgres(t))
	require.NoError(t, err)
	rec := &statementRecorder{}
	cfg.ConnConfig.Tracer = rec
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool, rec
}

// runPolledJob starts a worker with cfg on a traced pool, works one job
// enqueued after it started, and returns what the pool's connections ran.
func runPolledJob(t *testing.T, pollOnly bool) *statementRecorder {
	t.Helper()
	pool, rec := startTracedPool(t)
	cfg := worker.Config{Schema: "river", DefaultMaxWorkers: 2, PollOnly: pollOnly}
	api := migratedQueue(t, pool, cfg)

	w := &signalWorker{done: make(chan string, 1)}
	startConsumer(t, pool, cfg, w)

	_, err := api.Enqueue(context.Background(), signalArgs{Tag: "polled"}, nil)
	require.NoError(t, err)
	waitForJob(t, w.done, "polled")
	return rec
}

// PollOnly drops LISTEN/NOTIFY: the worker still finds a job enqueued after
// it started, by polling alone, and no connection of its pool ever runs
// LISTEN, which is what makes it usable behind transaction-mode PgBouncer.
func TestPollOnly_WorkerProcessesJobsWithoutListen(t *testing.T) {
	rec := runPolledJob(t, true)
	assert.False(t, rec.listens(), "a PollOnly worker must never run LISTEN")
}

// The same probe sees LISTEN when PollOnly is off, so the assertion above is
// not vacuous.
func TestPollOnlyOff_WorkerListens(t *testing.T) {
	rec := runPolledJob(t, false)
	require.Eventually(t, rec.listens, 10*time.Second, 50*time.Millisecond,
		"without PollOnly the worker's notifier runs LISTEN")
}
