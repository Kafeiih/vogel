package action_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/workflow"
)

// attemptDB records the INSERTs a Worker sends to its attempt log, keyed by
// the column names of the statement so tests do not depend on column order.
type attemptDB struct {
	mu   sync.Mutex
	rows []map[string]any
	err  error
}

var insertColumns = regexp.MustCompile(`(?s)INSERT INTO action_attempt \(([^)]*)\)`)

func (d *attemptDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return pgconn.CommandTag{}, d.err
	}
	m := insertColumns.FindStringSubmatch(sql)
	if m == nil {
		return pgconn.CommandTag{}, fmt.Errorf("unexpected statement: %s", sql)
	}
	row := map[string]any{}
	for i, col := range strings.Split(m[1], ",") {
		row[strings.TrimSpace(col)] = args[i]
	}
	d.rows = append(d.rows, row)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (d *attemptDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("not used")
}

func (d *attemptDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func (d *attemptDB) only(t *testing.T) map[string]any {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	require.Len(t, d.rows, 1)
	return d.rows[0]
}

func jobAt(args action.Args, attempt, maxAttempts int) *river.Job[action.Args] {
	return &river.Job[action.Args]{
		JobRow: &rivertype.JobRow{ID: 42, Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   args,
	}
}

func loggingWorker(t *testing.T, r *action.Registry, db *attemptDB) (*action.Worker, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	return action.NewWorker(r, action.WithAttemptLog(db), action.WithMetricsRegisterer(reg),
		action.WithLogger(slog.New(slog.DiscardHandler))), reg
}

func registerReturning(t *testing.T, r *action.Registry, name string, err error) {
	t.Helper()
	require.NoError(t, r.Register(name, action.HandlerFunc(func(context.Context, action.Run) error { return err })))
}

func TestWorker_RecordsSucceededAttempt(t *testing.T) {
	r, _ := newRegistry(t)
	registerReturning(t, r, "notify", nil)
	db := &attemptDB{}
	w, _ := loggingWorker(t, r, db)
	args := action.Args{CaseID: uuid.New(), Seq: 5, Effect: "notify", Part: 2, ActorID: "u1"}

	require.NoError(t, w.Work(context.Background(), jobAt(args, 1, 25)))

	row := db.only(t)
	assert.Equal(t, args.CaseID, row["case_id"])
	assert.Equal(t, int64(5), row["seq"])
	assert.Equal(t, "notify", row["effect"])
	assert.Equal(t, 2, row["part"])
	assert.Equal(t, 1, row["attempt"])
	assert.Equal(t, 0, row["snooze"])
	assert.Equal(t, int64(42), row["job_id"])
	assert.Equal(t, "succeeded", row["outcome"])
	assert.Equal(t, "", row["error_class"])
	assert.NotEqual(t, uuid.Nil, row["id"])
	started, finished := row["started_at"].(time.Time), row["finished_at"].(time.Time)
	assert.False(t, started.IsZero())
	assert.False(t, finished.Before(started))
}

func TestWorker_RecordsRetryWithErrorClassNotMessage(t *testing.T) {
	r, _ := newRegistry(t)
	registerReturning(t, r, "notify", fmt.Errorf("smtp: %w", errors.New("mailbox maria@example.cl rejected")))
	registerReturning(t, r, "slow", fmt.Errorf("partner timed out for maria: %w", context.DeadlineExceeded))
	db := &attemptDB{}
	w, _ := loggingWorker(t, r, db)

	require.Error(t, w.Work(context.Background(), jobAt(action.Args{Effect: "notify"}, 1, 25)))
	require.Error(t, w.Work(context.Background(), jobAt(action.Args{Effect: "slow"}, 2, 25)))

	require.Len(t, db.rows, 2)
	assert.Equal(t, "retry", db.rows[0]["outcome"])
	assert.Equal(t, "*errors.errorString", db.rows[0]["error_class"])
	assert.Equal(t, "retry", db.rows[1]["outcome"])
	assert.Equal(t, "context.deadlineExceededError (context.DeadlineExceeded)", db.rows[1]["error_class"])
	for _, row := range db.rows {
		assert.NotContains(t, row["error_class"], "maria", "the error message must never be stored")
	}
}

func TestWorker_RecordsCancelledOnJobCancel(t *testing.T) {
	r, _ := newRegistry(t)
	registerReturning(t, r, "notify", river.JobCancel(errors.New("bad address")))
	db := &attemptDB{}
	w, _ := loggingWorker(t, r, db)

	require.Error(t, w.Work(context.Background(), jobAt(action.Args{Effect: "notify"}, 1, 25)))

	row := db.only(t)
	assert.Equal(t, "cancelled", row["outcome"])
	assert.Equal(t, "*errors.errorString", row["error_class"])
}

func TestWorker_RecordsDiscardedOnLastAttempt(t *testing.T) {
	r, _ := newRegistry(t)
	registerReturning(t, r, "notify", errors.New("smtp down"))
	db := &attemptDB{}
	w, _ := loggingWorker(t, r, db)

	require.Error(t, w.Work(context.Background(), jobAt(action.Args{Effect: "notify"}, 25, 25)))

	assert.Equal(t, "discarded", db.only(t)["outcome"])
}

func TestWorker_RecordsSnoozedWithSnoozeCount(t *testing.T) {
	r, _ := newRegistry(t)
	registerReturning(t, r, "notify", river.JobSnooze(time.Minute))
	db := &attemptDB{}
	w, _ := loggingWorker(t, r, db)
	j := jobAt(action.Args{Effect: "notify"}, 3, 25)
	j.Metadata = []byte(`{"snoozes": 2}`)

	require.Error(t, w.Work(context.Background(), j))

	row := db.only(t)
	assert.Equal(t, "snoozed", row["outcome"])
	assert.Equal(t, 2, row["snooze"], "River gives a snoozed job its attempt back: the snooze count keeps the next run's row distinct")
}

func TestWorker_RecordsUnknownEffect(t *testing.T) {
	r, _ := newRegistry(t)
	db := &attemptDB{}
	w, _ := loggingWorker(t, r, db)

	err := w.Work(context.Background(), jobAt(action.Args{CaseID: uuid.New(), Effect: "missing"}, 1, 25))

	require.ErrorIs(t, err, workflow.ErrUnknownEffect)
	row := db.only(t)
	assert.Equal(t, "unknown_effect", row["outcome"])
	assert.Equal(t, "missing", row["effect"])
	assert.Equal(t, "*errors.errorString (workflow.ErrUnknownEffect)", row["error_class"])
}

func TestWorker_RecordsPanicAndRepanics(t *testing.T) {
	r, _ := newRegistry(t)
	require.NoError(t, r.Register("notify", action.HandlerFunc(func(context.Context, action.Run) error { panic("boom") })))
	db := &attemptDB{}
	w, _ := loggingWorker(t, r, db)

	assert.PanicsWithValue(t, "boom", func() {
		_ = w.Work(context.Background(), jobAt(action.Args{Effect: "notify"}, 1, 25))
	})

	row := db.only(t)
	assert.Equal(t, "retry", row["outcome"])
	assert.Equal(t, "panic", row["error_class"])
}

func TestWorker_AttemptLogFailureDoesNotFailJob(t *testing.T) {
	r, _ := newRegistry(t)
	registerReturning(t, r, "notify", nil)
	db := &attemptDB{err: errors.New("connection refused")}
	reg := prometheus.NewRegistry()
	var logs bytes.Buffer
	w := action.NewWorker(r, action.WithAttemptLog(db), action.WithMetricsRegisterer(reg),
		action.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	caseID := uuid.New()

	require.NoError(t, w.Work(context.Background(), jobAt(action.Args{CaseID: caseID, Seq: 3, Effect: "notify"}, 1, 25)))

	count, err := testutil.GatherAndCount(reg, "vogel_action_attempt_log_errors_total")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Equal(t, 1.0, gatherCounter(t, reg, "vogel_action_attempt_log_errors_total"))
	assert.Contains(t, logs.String(), caseID.String())
	assert.Contains(t, logs.String(), "outcome=succeeded")
	assert.Contains(t, logs.String(), "job_id=42")
}

func TestWorker_WithoutAttemptLogWritesNothing(t *testing.T) {
	r, _ := newRegistry(t)
	ran := false
	require.NoError(t, r.Register("notify", action.HandlerFunc(func(context.Context, action.Run) error {
		ran = true
		return nil
	})))
	reg := prometheus.NewRegistry()

	require.NoError(t, action.NewWorker(r, action.WithMetricsRegisterer(reg)).Work(context.Background(), jobAt(action.Args{Effect: "notify"}, 1, 25)))

	assert.True(t, ran)
	count, err := testutil.GatherAndCount(reg)
	require.NoError(t, err)
	assert.Zero(t, count, "without WithAttemptLog the worker registers no attempt-log metric")
}

func gatherCounter(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatalf("metric %s not found", name)
	return 0
}
