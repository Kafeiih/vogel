package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jobDurationSample is one river_job_duration_seconds series as gathered
// from a registry.
type jobDurationSample struct {
	count uint64
	sum   float64
}

// gatherJobDurations returns every river_job_duration_seconds series on reg,
// keyed by its {kind, outcome} labels.
func gatherJobDurations(t *testing.T, reg *prometheus.Registry) map[[2]string]jobDurationSample {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)

	out := map[[2]string]jobDurationSample{}
	for _, mf := range families {
		if mf.GetName() != jobDurationMetricName {
			continue
		}
		require.Equal(t, dto.MetricType_HISTOGRAM, mf.GetType())
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			require.Len(t, labels, 2, "only kind and outcome labels are expected")
			out[[2]string{labels["kind"], labels["outcome"]}] = jobDurationSample{
				count: m.GetHistogram().GetSampleCount(),
				sum:   m.GetHistogram().GetSampleSum(),
			}
		}
	}
	return out
}

// newTestMetricsMiddleware builds a metricsMiddleware on a fresh registry
// with a clock the test advances by hand.
func newTestMetricsMiddleware(t *testing.T) (*metricsMiddleware, *prometheus.Registry, *time.Time) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := newMetricsMiddleware(reg)
	require.NoError(t, err)
	clock := time.Unix(1_700_000_000, 0)
	m.now = func() time.Time { return clock }
	return m, reg, &clock
}

func TestMetricsMiddleware_ImplementsWorkerMiddleware(t *testing.T) {
	var _ rivertype.WorkerMiddleware = (*metricsMiddleware)(nil)
}

func TestMetricsMiddleware_RecordsOutcomeAndDuration(t *testing.T) {
	plainErr := errors.New("boom")
	cancelErr := river.JobCancel(errors.New("give up"))
	snoozeErr := river.JobSnooze(time.Minute)

	tests := []struct {
		name        string
		returned    error
		wantOutcome string
	}{
		{name: "nil is success", returned: nil, wantOutcome: "success"},
		{name: "plain error is error", returned: plainErr, wantOutcome: "error"},
		{name: "JobCancel is cancel", returned: cancelErr, wantOutcome: "cancel"},
		{name: "JobSnooze is snooze", returned: snoozeErr, wantOutcome: "snooze"},
		// River detects both with errors.As, so a wrapped one is still a
		// cancel or a snooze, not an error.
		{name: "wrapped JobCancel is cancel", returned: errWrap(cancelErr), wantOutcome: "cancel"},
		{name: "wrapped JobSnooze is snooze", returned: errWrap(snoozeErr), wantOutcome: "snooze"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, reg, clock := newTestMetricsMiddleware(t)
			job := &rivertype.JobRow{Kind: "send_email"}

			err := m.Work(context.Background(), job, func(context.Context) error {
				*clock = clock.Add(2 * time.Second)
				return tt.returned
			})

			if tt.returned == nil {
				assert.NoError(t, err)
			} else {
				assert.Same(t, tt.returned, err, "the inner error must be returned unchanged")
			}
			got := gatherJobDurations(t, reg)
			assert.Equal(t, map[[2]string]jobDurationSample{
				{"send_email", tt.wantOutcome}: {count: 1, sum: 2},
			}, got)
		})
	}
}

func TestMetricsMiddleware_PassesContextThrough(t *testing.T) {
	m, _, _ := newTestMetricsMiddleware(t)
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "v")

	var seen any
	require.NoError(t, m.Work(ctx, &rivertype.JobRow{Kind: "k"}, func(inner context.Context) error {
		seen = inner.Value(key{})
		return nil
	}))
	assert.Equal(t, "v", seen)
}

// River recovers a panicking Work outside the middleware chain and fails
// the attempt; the middleware must still record it, as outcome panic, and
// let the panic continue to River.
func TestMetricsMiddleware_PanicIsRecordedAndPropagated(t *testing.T) {
	m, reg, clock := newTestMetricsMiddleware(t)

	assert.PanicsWithValue(t, "kaboom", func() {
		_ = m.Work(context.Background(), &rivertype.JobRow{Kind: "k"}, func(context.Context) error {
			*clock = clock.Add(time.Second)
			panic("kaboom")
		})
	})

	assert.Equal(t, map[[2]string]jobDurationSample{
		{"k", "panic"}: {count: 1, sum: 1},
	}, gatherJobDurations(t, reg))
}

func TestNewMetricsMiddleware_DoubleRegistrationReusesCollector(t *testing.T) {
	reg := prometheus.NewRegistry()
	first, err := newMetricsMiddleware(reg)
	require.NoError(t, err)
	second, err := newMetricsMiddleware(reg)
	require.NoError(t, err, "a second registration on the same registry must not fail")
	assert.Same(t, first.duration, second.duration, "the already registered collector must be reused")

	job := &rivertype.JobRow{Kind: "k"}
	require.NoError(t, first.Work(context.Background(), job, func(context.Context) error { return nil }))
	require.NoError(t, second.Work(context.Background(), job, func(context.Context) error { return nil }))
	assert.Equal(t, uint64(2), gatherJobDurations(t, reg)[[2]string{"k", "success"}].count)
}

func TestNewMetricsMiddleware_ConflictingRegistrationFails(t *testing.T) {
	reg := prometheus.NewRegistry()
	// Same name, different label set: not the same collector, so it cannot
	// be reused and the conflict must surface.
	require.NoError(t, reg.Register(prometheus.NewHistogramVec(
		prometheus.HistogramOpts{Name: jobDurationMetricName, Help: "other"},
		[]string{"queue"},
	)))

	_, err := newMetricsMiddleware(reg)
	require.Error(t, err)
}

func TestNewOptions_WithMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	o := newOptions(WithMetrics(reg))
	assert.True(t, o.metrics)
	assert.Same(t, reg, o.metricsRegisterer)
}

func TestNewOptions_Default_NoMetrics(t *testing.T) {
	o := newOptions()
	assert.False(t, o.metrics)
}

func errWrap(err error) error { return &wrapErr{err} }

type wrapErr struct{ err error }

func (w *wrapErr) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapErr) Unwrap() error { return w.err }
