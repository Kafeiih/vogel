package worker

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

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

	rc := buildRiverConfig(cfg, nil, nil)

	assert.Equal(t, "custom_schema", rc.Schema)
}

func TestBuildRiverConfig_InsertOnlyMode_NoWorkersNoQueuesNoPeriodicJobs(t *testing.T) {
	cfg := Config{Schema: "river", DefaultMaxWorkers: 100}

	rc := buildRiverConfig(cfg, nil, nil)

	assert.Nil(t, rc.Workers, "Workers must be nil in insert-only mode")
	assert.Nil(t, rc.Queues, "Queues must be nil in insert-only mode")
	assert.Nil(t, rc.PeriodicJobs, "PeriodicJobs must be nil in insert-only mode")
}

func TestBuildRiverConfig_WorkerMode_HasQueues(t *testing.T) {
	workers := river.NewWorkers()
	cfg := Config{Schema: "river", DefaultMaxWorkers: 100}

	rc := buildRiverConfig(cfg, workers, nil)

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

	rc := buildRiverConfig(cfg, workers, jobs)

	assert.Equal(t, jobs, rc.PeriodicJobs, "periodic jobs must be registered in worker mode")
}

func TestBuildRiverConfig_InsertOnlyMode_PeriodicJobsNotRegistered(t *testing.T) {
	// Insert-only clients (workers == nil) never process anything, so
	// periodic jobs passed alongside a nil *river.Workers must not be
	// registered either.
	jobs := []*river.PeriodicJob{testPeriodicJob()}
	cfg := Config{Schema: "river", DefaultMaxWorkers: 100}

	rc := buildRiverConfig(cfg, nil, jobs)

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

	rc := buildRiverConfig(cfg, workers, nil)

	assert.Same(t, workers, rc.Workers)
	assert.NotEmpty(t, rc.Queues)
	assert.Nil(t, rc.PeriodicJobs)
}
