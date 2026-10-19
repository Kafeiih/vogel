package worker

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// jobDurationMetricName is the histogram metricsMiddleware records.
const jobDurationMetricName = "river_job_duration_seconds"

// Outcomes recorded in the outcome label. The set is closed, so the label
// stays bounded whatever a worker returns.
const (
	// outcomeSuccess: Work returned nil and the job is completed.
	outcomeSuccess = "success"
	// outcomeError: Work returned any other error; River retries the job or,
	// on its last attempt, discards it.
	outcomeError = "error"
	// outcomeCancel: Work returned river.JobCancel; River cancels the job
	// whatever attempts it has left.
	outcomeCancel = "cancel"
	// outcomeSnooze: Work returned river.JobSnooze; River schedules the job
	// again without counting the attempt as a failure.
	outcomeSnooze = "snooze"
	// outcomePanic: Work panicked; River recovers it and fails the attempt
	// like an error.
	outcomePanic = "panic"
)

// jobDurationBuckets span 10ms to 10min: jobs range from a quick insert to a
// report or a batch email, far longer than an HTTP request.
var jobDurationBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600}

// newJobDurationHistogram returns the unregistered river_job_duration_seconds
// collector.
func newJobDurationHistogram() *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    jobDurationMetricName,
			Help:    "Duration of River job attempts in seconds, by job kind and outcome (success, error, cancel, snooze, panic).",
			Buckets: jobDurationBuckets,
		},
		[]string{"kind", "outcome"},
	)
}

// metricsMiddleware records one river_job_duration_seconds observation per
// job attempt, labeled by the job's kind and the outcome of its Work.
//
// The kind label is the job's Kind(), a constant per JobArgs type, so the
// label set is bounded by the job types the worker registers. The histogram's
// _count by outcome is also the attempt counter: the error rate of a kind is
// rate(..._count{outcome=~"error|panic"}) over rate(..._count), so no
// separate counter is exported.
type metricsMiddleware struct {
	river.MiddlewareDefaults

	duration *prometheus.HistogramVec
	// now is time.Now outside tests.
	now func() time.Time
}

var _ rivertype.WorkerMiddleware = (*metricsMiddleware)(nil)

// newMetricsMiddleware registers river_job_duration_seconds on reg and returns
// a middleware that records into it. A prometheus.AlreadyRegisteredError is
// handled by reusing the registered collector, as postgres.NewPoolMetricsCollector
// and httpx/middleware.NewMetrics do, so building two worker queues against
// one Registerer (a test per case, two queues in one process) does not fail.
func newMetricsMiddleware(reg prometheus.Registerer) (*metricsMiddleware, error) {
	duration := newJobDurationHistogram()
	if err := reg.Register(duration); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			return nil, err
		}
		existing, ok := are.ExistingCollector.(*prometheus.HistogramVec)
		if !ok {
			return nil, err
		}
		duration = existing
	}
	return &metricsMiddleware{duration: duration, now: time.Now}, nil
}

// Work times the rest of the chain and the worker's Work. The error is
// returned unchanged. A panic is recorded with outcome panic and left to
// propagate: River recovers it outside the middleware chain.
func (m *metricsMiddleware) Work(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
	start := m.now()
	outcome := outcomePanic
	defer func() {
		m.duration.WithLabelValues(job.Kind, outcome).Observe(m.now().Sub(start).Seconds())
	}()

	err := doInner(ctx)
	outcome = jobOutcome(err)
	return err
}

// jobOutcome classifies what a worker's Work returned the way River's
// executor does: snooze and cancel are detected with errors.As, so a wrapped
// one still counts as such.
func jobOutcome(err error) string {
	if err == nil {
		return outcomeSuccess
	}
	var snoozeErr *rivertype.JobSnoozeError
	if errors.As(err, &snoozeErr) {
		return outcomeSnooze
	}
	var cancelErr *rivertype.JobCancelError
	if errors.As(err, &cancelErr) {
		return outcomeCancel
	}
	return outcomeError
}
