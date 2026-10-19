package otel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/kafeiih/vogel/httpx/middleware"
	"github.com/kafeiih/vogel/notification"
)

var (
	_ middleware.ExemplarFunc                = SampledTraceIDs
	_ func(context.Context) (string, string) = SampledTraceIDs
)

func TestSampledTraceIDs_OnlyForSampledSpans(t *testing.T) {
	sampled := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
	})
	unsampled := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})

	traceID, spanID := SampledTraceIDs(trace.ContextWithSpanContext(context.Background(), sampled))
	if traceID != sampled.TraceID().String() || spanID != sampled.SpanID().String() {
		t.Errorf("sampled: got (%q, %q), want the span's IDs", traceID, spanID)
	}
	if traceID, spanID := SampledTraceIDs(trace.ContextWithSpanContext(context.Background(), unsampled)); traceID != "" || spanID != "" {
		t.Errorf("unsampled: got (%q, %q), want empty: an exemplar must point at an exported trace", traceID, spanID)
	}
	if traceID, _ := SampledTraceIDs(context.Background()); traceID != "" {
		t.Errorf("no span: got %q, want empty", traceID)
	}
}

// Transport records a client span per request and sends the traceparent,
// but not the caller's baggage: the outbound clients talk to third parties.
func TestTransport_PropagatesTheTraceButNotBaggage(t *testing.T) {
	rec, tp := newRecorder(t)
	var traceparent, baggageHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("traceparent")
		baggageHeader = r.Header.Get("baggage")
	}))
	defer srv.Close()

	member, _ := baggage.NewMember("user", "alice")
	bag, _ := baggage.New(member)
	ctx, parent := tp.Tracer("test").Start(baggage.ContextWithBaggage(context.Background(), bag), "parent")
	client := &http.Client{Transport: Transport(WithTracerProvider(tp))(http.DefaultTransport)}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v2/users?id=42", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	parent.End()

	var span sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.SpanKind() == trace.SpanKindClient {
			span = s
		}
	}
	if span == nil {
		t.Fatal("no client span recorded")
	}
	if span.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Error("the client span is not a child of the caller's span")
	}
	if !strings.Contains(traceparent, span.SpanContext().TraceID().String()) {
		t.Errorf("traceparent = %q, want the trace ID", traceparent)
	}
	if baggageHeader != "" {
		t.Errorf("baggage = %q, want none sent to a third party", baggageHeader)
	}
	if span.Name() != "HTTP GET" && span.Name() != "GET" {
		t.Errorf("span name = %q, want the method only (the path is high-cardinality)", span.Name())
	}
}

type fakeNotifier struct{ err error }

func (n fakeNotifier) Send(context.Context, *notification.Message) error { return n.err }

// Notifier records a span per Send, with no recipient, sender or subject:
// those are personal data.
func TestNotifier_SpanWithoutPersonalData(t *testing.T) {
	rec, tp := newRecorder(t)
	msg := &notification.Message{To: "alice@example.com", From: "bob@example.com", Subject: "Your case 4711", Text: "hi"}

	if err := Notifier(fakeNotifier{}, WithTracerProvider(tp)).Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sendErr := errors.New("smtp: 550 mailbox unavailable")
	if err := Notifier(fakeNotifier{err: sendErr}, WithTracerProvider(tp)).Send(context.Background(), msg); !errors.Is(err, sendErr) {
		t.Fatalf("Send error = %v, want the notifier's error", err)
	}

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(spans))
	}
	for _, s := range spans {
		if s.Name() != "notification.send" || s.SpanKind() != trace.SpanKindClient {
			t.Errorf("span = %q (%v), want notification.send (client)", s.Name(), s.SpanKind())
		}
		for _, kv := range s.Attributes() {
			v := kv.Value.String()
			if strings.Contains(v, "alice") || strings.Contains(v, "bob") || strings.Contains(v, "4711") {
				t.Errorf("attribute %s = %q leaks personal data", kv.Key, v)
			}
		}
	}
	if spans[0].Status().Code != codes.Unset || spans[1].Status().Code != codes.Error {
		t.Errorf("statuses = (%v, %v), want (Unset, Error)", spans[0].Status().Code, spans[1].Status().Code)
	}
	for _, e := range spans[1].Events() {
		for _, kv := range e.Attributes {
			if strings.Contains(kv.Value.String(), "mailbox") {
				t.Errorf("error event %s = %q: a delivery error can quote the address", kv.Key, kv.Value.String())
			}
		}
	}
}
