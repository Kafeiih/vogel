// Package otelriver traces River jobs with OpenTelemetry and carries the
// trace from the process that enqueues a job to the one that works it. Plug
// its Middleware into worker.WithMiddleware:
//
//	queue, err := worker.NewRiverQueue(pool, workers, cfg, logger,
//		worker.WithMiddleware(otelriver.NewMiddleware()))
//
// On insert it records a producer span and stamps its W3C traceparent and
// tracestate into each job's metadata, under vogel_traceparent and
// vogel_tracestate, next to vogel_request_id. On work it records a consumer
// span that continues that trace, so the job shows up under the request that
// enqueued it. Job metadata is written by the service itself, not by
// clients, so it is trusted the way the HTTP middleware's default does not
// trust an incoming traceparent. Baggage is not carried: it would be stored
// in the job table in plain text.
package otelriver

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	gootel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/kafeiih/vogel/otel/otelriver"

// metadataPrefix namespaces the propagation keys in the job metadata, as
// vogel_request_id is, so they do not collide by accident with metadata a
// consumer sets through river.InsertOpts.Metadata. A consumer that sets
// vogel_traceparent on purpose wins (see withTraceContext).
const (
	metadataPrefix = "vogel_"
	traceParentKey = metadataPrefix + "traceparent"
	traceStateKey  = metadataPrefix + "tracestate"
)

var propagator = propagation.TraceContext{}

// Option configures a Middleware.
type Option func(*Middleware)

// WithTracerProvider sets the TracerProvider the Middleware uses instead of
// the global one otel.Setup installs.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(m *Middleware) { m.tp = tp }
}

// Middleware is a River job insert and worker middleware. Build it with
// NewMiddleware.
type Middleware struct {
	river.MiddlewareDefaults
	tp trace.TracerProvider // nil: the global one, read on every span
}

var (
	_ rivertype.JobInsertMiddleware = (*Middleware)(nil)
	_ rivertype.WorkerMiddleware    = (*Middleware)(nil)
)

// NewMiddleware returns a Middleware. The global TracerProvider is read when
// each span starts, not here, so NewMiddleware may run before otel.Setup.
func NewMiddleware(opts ...Option) *Middleware {
	m := &Middleware{}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *Middleware) tracer() trace.Tracer {
	tp := m.tp
	if tp == nil {
		tp = gootel.GetTracerProvider()
	}
	return tp.Tracer(instrumentationName)
}

// InsertMany records one producer span for the insert and stamps its
// context into every job's metadata. A job whose metadata already carries
// vogel_traceparent, or is not a JSON object, is inserted unchanged.
func (m *Middleware) InsertMany(ctx context.Context, manyParams []*rivertype.JobInsertParams, doInner func(context.Context) ([]*rivertype.JobInsertResult, error)) ([]*rivertype.JobInsertResult, error) {
	attrs := []attribute.KeyValue{
		semconv.MessagingSystemKey.String("river"),
		semconv.MessagingOperationName("insert"),
		semconv.MessagingOperationTypeSend,
		semconv.MessagingBatchMessageCount(len(manyParams)),
	}
	// The span name must stay low-cardinality: the job kind when the insert
	// has one, "jobs" when it mixes kinds.
	target := "jobs"
	if kind := commonKind(manyParams); kind != "" {
		attrs = append(attrs, attribute.String("river.job.kind", kind))
		target = kind
	}
	ctx, span := m.tracer().Start(ctx, "insert "+target,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(attrs...))
	defer span.End()

	carrier := propagation.MapCarrier{}
	propagator.Inject(ctx, carrier)
	if len(carrier) > 0 {
		for _, p := range manyParams {
			p.Metadata = withTraceContext(p.Metadata, carrier)
		}
	}

	results, err := doInner(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return results, err
}

// Work records a consumer span for the job attempt, continuing the trace
// stamped at insert when there is one.
func (m *Middleware) Work(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
	ctx = propagator.Extract(ctx, traceContextFromMetadata(job.Metadata))
	ctx, span := m.tracer().Start(ctx, "process "+job.Kind,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String("river"),
			semconv.MessagingOperationName("process"),
			semconv.MessagingOperationTypeProcess,
			semconv.MessagingDestinationName(job.Queue),
			semconv.MessagingMessageID(strconv.FormatInt(job.ID, 10)),
			attribute.String("river.job.kind", job.Kind),
			attribute.Int("river.job.attempt", job.Attempt),
		))
	defer span.End()

	err := doInner(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

// commonKind returns the job kind shared by every job of the insert, or ""
// when they differ.
func commonKind(manyParams []*rivertype.JobInsertParams) string {
	if len(manyParams) == 0 {
		return ""
	}
	kind := manyParams[0].Kind
	for _, p := range manyParams[1:] {
		if p.Kind != kind {
			return ""
		}
	}
	return kind
}

// withTraceContext returns metadata with the carrier's keys added under
// metadataPrefix. Metadata that is not a JSON object, or that already
// carries a traceparent (set explicitly through river.InsertOpts.Metadata),
// is returned unchanged: an explicit value wins, as it does for the request
// ID.
func withTraceContext(metadata []byte, carrier propagation.MapCarrier) []byte {
	fields := map[string]json.RawMessage{}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &fields); err != nil || fields == nil {
			return metadata
		}
	}
	if _, ok := fields[traceParentKey]; ok {
		return metadata
	}
	for k, v := range carrier {
		encoded, err := json.Marshal(v)
		if err != nil {
			return metadata
		}
		fields[metadataPrefix+k] = encoded
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return metadata
	}
	return out
}

// traceContextFromMetadata returns the traceparent and tracestate stamped by
// withTraceContext, as a carrier the propagator reads. Missing or malformed
// metadata gives an empty carrier, and the job starts a new trace.
func traceContextFromMetadata(metadata []byte) propagation.MapCarrier {
	carrier := propagation.MapCarrier{}
	if len(metadata) == 0 {
		return carrier
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &fields); err != nil {
		return carrier
	}
	for carrierKey, metadataKey := range map[string]string{"traceparent": traceParentKey, "tracestate": traceStateKey} {
		var v string
		if json.Unmarshal(fields[metadataKey], &v) == nil && v != "" {
			carrier[carrierKey] = v
		}
	}
	return carrier
}
