package worker

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------
// isValidSchemaName / EnsureSchema
// -----------------------------------------------------------------------

func TestIsValidSchemaName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid simple", "river", true},
		{"valid with underscore", "river_queue", true},
		{"valid starting with underscore", "_internal", true},
		{"valid with digits after letter", "river_v2", true},
		{"valid uppercase", "River", true},
		{"max length 63 chars", strings.Repeat("a", 63), true},
		{"empty rejected", "", false},
		{"starts with digit rejected", "1river", false},
		{"contains semicolon rejected (sql injection)", "river; DROP TABLE x", false},
		{"contains space rejected", "river queue", false},
		{"contains hyphen rejected", "river-queue", false},
		{"contains quote rejected", `river"`, false},
		{"contains dot rejected", "river.public", false},
		{"too long 64 chars rejected", strings.Repeat("a", 64), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isValidSchemaName(tt.input))
		})
	}
}

func TestEnsureSchema_RejectsInvalidName(t *testing.T) {
	// Malicious name must error BEFORE touching the pool. nil pool is
	// intentional: if validation fails first, no pool call happens.
	err := EnsureSchema(context.Background(), nil, "river; DROP TABLE x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid schema name")
}

func TestEnsureSchema_RejectsEmptyName(t *testing.T) {
	err := EnsureSchema(context.Background(), nil, "")
	require.Error(t, err)
}

func TestEnsureSchema_RejectsNilPool(t *testing.T) {
	// Valid name but nil pool: the pool nil-check runs after validation.
	err := EnsureSchema(context.Background(), nil, "river")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pool is nil")
}

// -----------------------------------------------------------------------
// NewRiverQueue
// -----------------------------------------------------------------------

func TestNewRiverQueue_NilPool(t *testing.T) {
	logger := slog.Default()
	cfg := Config{Schema: "river", DefaultMaxWorkers: 100}

	_, err := NewRiverQueue(nil, nil, cfg, logger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pgxpool.Pool is required")
}

func TestNewRiverQueue_NilLogger(t *testing.T) {
	// We can't create a real pool without a DB, but the nil-pool check comes
	// first regardless, so this still proves a nil logger is caught before
	// any river.Client is built.
	cfg := Config{Schema: "river", DefaultMaxWorkers: 100}

	_, err := NewRiverQueue(nil, nil, cfg, nil)
	require.Error(t, err)
}

// -----------------------------------------------------------------------
// buildQueueConfig
// -----------------------------------------------------------------------

func TestBuildQueueConfig_Defaults(t *testing.T) {
	cfg := Config{DefaultMaxWorkers: 100}

	queues := buildQueueConfig(cfg)

	require.Len(t, queues, 1)
	defaultQ, ok := queues[river.QueueDefault]
	require.True(t, ok, "expected default queue")
	assert.Equal(t, 100, defaultQ.MaxWorkers)
}

func TestBuildQueueConfig_CustomQueues(t *testing.T) {
	cfg := Config{
		DefaultMaxWorkers: 100,
		Queues:            map[string]int{"critical": 20, "low": 5},
	}

	queues := buildQueueConfig(cfg)

	require.Len(t, queues, 2)
	assert.Equal(t, 20, queues["critical"].MaxWorkers)
	assert.Equal(t, 5, queues["low"].MaxWorkers)
}

// -----------------------------------------------------------------------
// buildRiverConfig
// -----------------------------------------------------------------------

func TestBuildRiverConfig_SchemaPropagates(t *testing.T) {
	cfg := Config{Schema: "custom_schema", DefaultMaxWorkers: 100}

	rc := buildRiverConfig(cfg, nil, nil, nil, nil)

	assert.Equal(t, "custom_schema", rc.Schema)
}

func TestBuildRiverConfig_InsertOnlyMode_NoWorkersNoQueuesNoPeriodicJobs(t *testing.T) {
	cfg := Config{Schema: "river", DefaultMaxWorkers: 100}

	rc := buildRiverConfig(cfg, nil, nil, nil, nil)

	assert.Nil(t, rc.Workers, "Workers must be nil in insert-only mode")
	assert.Nil(t, rc.Queues, "Queues must be nil in insert-only mode")
	assert.Nil(t, rc.PeriodicJobs, "PeriodicJobs must be nil in insert-only mode")
}

func TestBuildRiverConfig_WorkerMode_HasQueues(t *testing.T) {
	workers := river.NewWorkers()
	cfg := Config{Schema: "river", DefaultMaxWorkers: 100}

	rc := buildRiverConfig(cfg, workers, nil, nil, nil)

	assert.Same(t, workers, rc.Workers, "Workers must be set in worker mode")
	assert.NotEmpty(t, rc.Queues, "Queues must be set in worker mode")
	assert.Equal(t, "river", rc.Schema)
}

// -----------------------------------------------------------------------
// WithPeriodicJobs / newOptions
//
// periodicJobs is a functional option (WithPeriodicJobs), not a positional
// NewRiverQueue parameter, so both source repositories' call shapes keep
// working: go-licencias never passes the option and gets the exact same
// behavior it always had (no periodic jobs registered), go-crucible adds
// worker.WithPeriodicJobs(jobs).
// -----------------------------------------------------------------------

func testPeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(1*time.Hour),
		func() (river.JobArgs, *river.InsertOpts) {
			return testJobArgs{}, nil
		},
		&river.PeriodicJobOpts{RunOnStart: false},
	)
}

type testJobArgs struct{}

func (testJobArgs) Kind() string { return "test_job" }

func TestNewOptions_WithPeriodicJobs(t *testing.T) {
	jobs := []*river.PeriodicJob{testPeriodicJob()}

	o := newOptions(WithPeriodicJobs(jobs))

	assert.Equal(t, jobs, o.periodicJobs)
}

func TestNewOptions_Default_NoPeriodicJobs(t *testing.T) {
	// No WithPeriodicJobs passed: this is the go-licencias call shape.
	o := newOptions()

	assert.Nil(t, o.periodicJobs)
}

func TestBuildRiverConfig_WorkerMode_WithPeriodicJobs(t *testing.T) {
	workers := river.NewWorkers()
	jobs := []*river.PeriodicJob{testPeriodicJob()}
	cfg := Config{Schema: "river", DefaultMaxWorkers: 100}

	rc := buildRiverConfig(cfg, workers, jobs, nil, nil)

	assert.Equal(t, jobs, rc.PeriodicJobs, "periodic jobs must be registered in worker mode")
}

func TestBuildRiverConfig_InsertOnlyMode_PeriodicJobsNotRegistered(t *testing.T) {
	// Insert-only clients (workers == nil) never process anything, so
	// periodic jobs passed alongside a nil *river.Workers must not be
	// registered either.
	jobs := []*river.PeriodicJob{testPeriodicJob()}
	cfg := Config{Schema: "river", DefaultMaxWorkers: 100}

	rc := buildRiverConfig(cfg, nil, jobs, nil, nil)

	assert.Nil(t, rc.PeriodicJobs)
	assert.Nil(t, rc.Workers)
	assert.Nil(t, rc.Queues)
}

func TestBuildRiverConfig_WorkerMode_NoPeriodicJobsOption_MatchesLicenciasShape(t *testing.T) {
	// Equivalent to go-licencias calling buildRiverConfig(cfg, workers) with
	// no periodicJobs parameter at all: worker mode still wires up Workers
	// and Queues, but PeriodicJobs stays empty.
	workers := river.NewWorkers()
	cfg := Config{Schema: "river", DefaultMaxWorkers: 100}

	rc := buildRiverConfig(cfg, workers, nil, nil, nil)

	assert.Same(t, workers, rc.Workers)
	assert.NotEmpty(t, rc.Queues)
	assert.Nil(t, rc.PeriodicJobs)
}

// -----------------------------------------------------------------------
// WithMetrics / PollOnly in buildRiverConfig
// -----------------------------------------------------------------------

// Metrics wrap the work after the request ID middleware has put the job's
// request ID in the context: requestID first, metrics second.
func TestBuildRiverConfig_WorkerMode_WithMetrics_OrderedAfterRequestID(t *testing.T) {
	mw, err := newMetricsMiddleware(prometheus.NewRegistry())
	require.NoError(t, err)

	rc := buildRiverConfig(Config{Schema: "river"}, river.NewWorkers(), nil, mw, nil)

	require.Len(t, rc.Middleware, 2)
	assert.IsType(t, &requestIDMiddleware{}, rc.Middleware[0])
	assert.Same(t, mw, rc.Middleware[1])
}

// An insert-only client works no jobs, so the metrics middleware is not
// installed there even if one is passed, like periodic jobs.
func TestBuildRiverConfig_InsertOnlyMode_MetricsNotInstalled(t *testing.T) {
	mw, err := newMetricsMiddleware(prometheus.NewRegistry())
	require.NoError(t, err)

	rc := buildRiverConfig(Config{Schema: "river"}, nil, nil, mw, nil)

	require.Len(t, rc.Middleware, 1)
	assert.IsType(t, &requestIDMiddleware{}, rc.Middleware[0])
}

func TestBuildRiverConfig_WorkerMode_PollOnlyMapped(t *testing.T) {
	for _, pollOnly := range []bool{false, true} {
		rc := buildRiverConfig(Config{Schema: "river", PollOnly: pollOnly}, river.NewWorkers(), nil, nil, nil)
		assert.Equal(t, pollOnly, rc.PollOnly)
	}
}

// PollOnly only changes how a client that works jobs waits for them; an
// insert-only client never listens, so it is left unset there.
func TestBuildRiverConfig_InsertOnlyMode_PollOnlyNotSet(t *testing.T) {
	rc := buildRiverConfig(Config{Schema: "river", PollOnly: true}, nil, nil, nil, nil)
	assert.False(t, rc.PollOnly)
}

// Without WithMetrics no metrics middleware is built: the behavior before
// the option existed.
func TestOptionsBuildMetrics_WithoutOption_BuildsNothing(t *testing.T) {
	o := newOptions()
	mw, err := o.buildMetrics(river.NewWorkers())
	require.NoError(t, err)
	assert.Nil(t, mw)
}

func TestOptionsBuildMetrics_InsertOnly_RegistersNothing(t *testing.T) {
	reg := prometheus.NewRegistry()
	mw, err := newOptions(WithMetrics(reg)).buildMetrics(nil)
	require.NoError(t, err)
	assert.Nil(t, mw)

	families, err := reg.Gather()
	require.NoError(t, err)
	assert.Empty(t, families)
	// Registering again must still work: nothing was registered.
	_, err = newMetricsMiddleware(reg)
	require.NoError(t, err)
}

func TestOptionsBuildMetrics_WorkerMode_RegistersOnRegisterer(t *testing.T) {
	reg := prometheus.NewRegistry()
	mw, err := newOptions(WithMetrics(reg)).buildMetrics(river.NewWorkers())
	require.NoError(t, err)
	require.NotNil(t, mw)

	// Already registered: a second identical registration is reported as
	// such, proving the first one landed on reg.
	err = reg.Register(newJobDurationHistogram())
	var are prometheus.AlreadyRegisteredError
	assert.ErrorAs(t, err, &are)
}

// -----------------------------------------------------------------------
// WithMiddleware
// -----------------------------------------------------------------------

type stubMiddleware struct {
	river.MiddlewareDefaults
	name string
}

// Extra middlewares go after the request ID one (so they see the job's
// request ID) and before metrics (so a span they open is in the context
// metrics observes), in the order given, in both modes.
func TestWithMiddleware_OrderedBetweenRequestIDAndMetrics(t *testing.T) {
	mw, err := newMetricsMiddleware(prometheus.NewRegistry())
	require.NoError(t, err)
	a, b := &stubMiddleware{name: "a"}, &stubMiddleware{name: "b"}
	o := newOptions(WithMiddleware(a), WithMiddleware(b))

	rc := buildRiverConfig(Config{Schema: "river"}, river.NewWorkers(), nil, mw, nil)
	o.applyMiddleware(rc)

	require.Len(t, rc.Middleware, 4)
	assert.IsType(t, &requestIDMiddleware{}, rc.Middleware[0])
	assert.Same(t, a, rc.Middleware[1])
	assert.Same(t, b, rc.Middleware[2])
	assert.Same(t, mw, rc.Middleware[3])
}

// An insert-only client keeps extra middlewares: insert middlewares stamp
// the enqueuing context into the job, which is where tracing propagates.
func TestWithMiddleware_InsertOnlyModeKeepsThem(t *testing.T) {
	a := &stubMiddleware{name: "a"}
	o := newOptions(WithMiddleware(a))

	rc := buildRiverConfig(Config{Schema: "river"}, nil, nil, nil, nil)
	o.applyMiddleware(rc)

	require.Len(t, rc.Middleware, 2)
	assert.IsType(t, &requestIDMiddleware{}, rc.Middleware[0])
	assert.Same(t, a, rc.Middleware[1])
}

func TestWithMiddleware_NoneLeavesTheChainUnchanged(t *testing.T) {
	rc := buildRiverConfig(Config{Schema: "river"}, river.NewWorkers(), nil, nil, nil)
	newOptions().applyMiddleware(rc)
	require.Len(t, rc.Middleware, 1)
	assert.IsType(t, &requestIDMiddleware{}, rc.Middleware[0])
}
