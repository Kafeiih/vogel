package otelriver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/kafeiih/vogel/worker"
)

var _ = worker.WithMiddleware(NewMiddleware())

func newRecorder(t *testing.T, sampler sdktrace.Sampler) (*Middleware, *tracetest.SpanRecorder, trace.TracerProvider) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sampler))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return NewMiddleware(WithTracerProvider(tp)), rec, tp
}

func insert(t *testing.T, m *Middleware, ctx context.Context, params ...*rivertype.JobInsertParams) {
	t.Helper()
	_, err := m.InsertMany(ctx, params, func(context.Context) ([]*rivertype.JobInsertResult, error) { return nil, nil })
	require.NoError(t, err)
}

func metadataFields(t *testing.T, metadata []byte) map[string]any {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal(metadata, &fields))
	return fields
}

// A job enqueued inside a request is worked inside the same trace: the
// consumer span is a child of the producer span, which is a child of the
// request's span.
func TestInsertThenWork_ContinuesTheTrace(t *testing.T) {
	m, rec, tp := newRecorder(t, sdktrace.AlwaysSample())
	reqCtx, req := tp.Tracer("test").Start(context.Background(), "GET /orders")

	p := &rivertype.JobInsertParams{Kind: "send_email", Queue: "default", Metadata: []byte(`{"vogel_request_id":"req-1"}`)}
	insert(t, m, reqCtx, p)
	req.End()

	fields := metadataFields(t, p.Metadata)
	assert.Equal(t, "req-1", fields["vogel_request_id"], "existing metadata is kept")
	require.Contains(t, fields, "vogel_traceparent")

	var workCtx context.Context
	job := &rivertype.JobRow{ID: 42, Kind: "send_email", Queue: "default", Attempt: 2, Metadata: p.Metadata}
	require.NoError(t, m.Work(context.Background(), job, func(ctx context.Context) error {
		workCtx = ctx
		return nil
	}))

	spans := rec.Ended()
	require.Len(t, spans, 3)
	producer, consumer := spans[0], spans[2]
	assert.Equal(t, "insert send_email", producer.Name())
	assert.Equal(t, trace.SpanKindProducer, producer.SpanKind())
	assert.Equal(t, req.SpanContext().SpanID(), producer.Parent().SpanID())

	assert.Equal(t, "process send_email", consumer.Name())
	assert.Equal(t, trace.SpanKindConsumer, consumer.SpanKind())
	assert.Equal(t, req.SpanContext().TraceID(), consumer.SpanContext().TraceID())
	assert.Equal(t, producer.SpanContext().SpanID(), consumer.Parent().SpanID())
	assert.Equal(t, consumer.SpanContext().SpanID(), trace.SpanContextFromContext(workCtx).SpanID(),
		"the worker runs inside the consumer span")

	a := map[string]any{}
	for _, kv := range consumer.Attributes() {
		a[string(kv.Key)] = kv.Value.AsInterface()
	}
	assert.Equal(t, "river", a["messaging.system"])
	assert.Equal(t, "default", a["messaging.destination.name"])
	assert.Equal(t, "42", a["messaging.message.id"])
	assert.Equal(t, int64(2), a["river.job.attempt"])
}

func TestInsert_MixedKindsAreNamedJobs(t *testing.T) {
	m, rec, _ := newRecorder(t, sdktrace.AlwaysSample())

	insert(t, m, context.Background(), &rivertype.JobInsertParams{Kind: "a"}, &rivertype.JobInsertParams{Kind: "b"})

	assert.Equal(t, "insert jobs", rec.Ended()[0].Name())
}

// An unsampled request still stamps its traceparent, so the job is not
// sampled either: the trace's sampling decision holds across the queue.
func TestInsert_UnsampledTraceStaysUnsampled(t *testing.T) {
	m, rec, _ := newRecorder(t, sdktrace.ParentBased(sdktrace.NeverSample()))
	p := &rivertype.JobInsertParams{Kind: "k"}

	insert(t, m, context.Background(), p)
	require.Contains(t, metadataFields(t, p.Metadata), "vogel_traceparent")

	m2, rec2, _ := newRecorder(t, sdktrace.ParentBased(sdktrace.AlwaysSample()))
	require.NoError(t, m2.Work(context.Background(), &rivertype.JobRow{Kind: "k", Metadata: p.Metadata}, func(context.Context) error { return nil }))

	assert.Empty(t, rec.Ended())
	assert.Empty(t, rec2.Ended(), "the worker follows the enqueuer's not-sampled decision")
}

func TestInsert_ExplicitTraceparentWins(t *testing.T) {
	m, _, _ := newRecorder(t, sdktrace.AlwaysSample())
	const explicit = `{"vogel_traceparent":"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}`
	p := &rivertype.JobInsertParams{Kind: "k", Metadata: []byte(explicit)}

	insert(t, m, context.Background(), p)

	assert.JSONEq(t, explicit, string(p.Metadata))
}

func TestInsert_NonObjectMetadataIsUnchanged(t *testing.T) {
	m, _, _ := newRecorder(t, sdktrace.AlwaysSample())
	p := &rivertype.JobInsertParams{Kind: "k", Metadata: []byte(`[1,2]`)}

	insert(t, m, context.Background(), p)

	assert.Equal(t, `[1,2]`, string(p.Metadata))
}

// With tracing off (the no-op global provider otel.Setup leaves without an
// endpoint) there is no span context to stamp, and metadata is untouched.
func TestInsert_TracingOffLeavesMetadataAlone(t *testing.T) {
	m := NewMiddleware(WithTracerProvider(noop.NewTracerProvider()))
	p := &rivertype.JobInsertParams{Kind: "k"}

	insert(t, m, context.Background(), p)

	assert.Nil(t, p.Metadata)
}

func TestInsert_RecordsTheError(t *testing.T) {
	m, rec, _ := newRecorder(t, sdktrace.AlwaysSample())
	_, err := m.InsertMany(context.Background(), []*rivertype.JobInsertParams{{Kind: "k"}},
		func(context.Context) ([]*rivertype.JobInsertResult, error) { return nil, errors.New("db down") })

	require.EqualError(t, err, "db down")
	assert.Equal(t, codes.Error, rec.Ended()[0].Status().Code)
}

func TestWork_RecordsTheErrorAndReturnsIt(t *testing.T) {
	m, rec, _ := newRecorder(t, sdktrace.AlwaysSample())
	jobErr := errors.New("smtp timeout")

	err := m.Work(context.Background(), &rivertype.JobRow{Kind: "k"}, func(context.Context) error { return jobErr })

	require.ErrorIs(t, err, jobErr)
	s := rec.Ended()[0]
	assert.Equal(t, codes.Error, s.Status().Code)
	assert.False(t, s.Parent().IsValid(), "a job without a stamped trace starts a new one")
}

func TestWork_MalformedMetadataStartsANewTrace(t *testing.T) {
	m, rec, _ := newRecorder(t, sdktrace.AlwaysSample())

	for _, md := range []string{`not json`, `{"vogel_traceparent":42}`, `{"vogel_traceparent":"garbage"}`} {
		require.NoError(t, m.Work(context.Background(), &rivertype.JobRow{Kind: "k", Metadata: []byte(md)}, func(context.Context) error { return nil }))
	}

	for _, s := range rec.Ended() {
		assert.False(t, s.Parent().IsValid())
	}
	assert.Len(t, rec.Ended(), 3)
}

// Snoozing is River's way to retry later on purpose: not a failure.
func TestWork_SnoozeIsNotAnError(t *testing.T) {
	m, rec, _ := newRecorder(t, sdktrace.AlwaysSample())

	err := m.Work(context.Background(), &rivertype.JobRow{Kind: "k"}, func(context.Context) error { return river.JobSnooze(time.Minute) })

	var snooze *rivertype.JobSnoozeError
	require.ErrorAs(t, err, &snooze, "the snooze must still reach River")
	s := rec.Ended()[0]
	assert.Equal(t, codes.Unset, s.Status().Code)
	require.Len(t, s.Events(), 1)
	assert.Equal(t, "snoozed", s.Events()[0].Name)
}
