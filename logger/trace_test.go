package logger

import (
	"bytes"
	"context"
	"testing"
)

type traceKey struct{}

// fakeTraceIDs reads a "trace/span" pair stored under traceKey, standing in
// for an OpenTelemetry span context.
func fakeTraceIDs(ctx context.Context) (traceID, spanID string) {
	ids, _ := ctx.Value(traceKey{}).([2]string)
	return ids[0], ids[1]
}

func withTrace(ctx context.Context, traceID, spanID string) context.Context {
	return context.WithValue(ctx, traceKey{}, [2]string{traceID, spanID})
}

func TestWithTraceIDs_InfoContextAddsTraceAndSpanID(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf, WithTraceIDs(fakeTraceIDs))

	l.InfoContext(withTrace(requestContext("req-1", "u-1"), "t-1", "s-1"), "hello")

	got := decodeJSONLine(t, &buf)
	if got["trace_id"] != "t-1" || got["span_id"] != "s-1" {
		t.Errorf("(trace_id, span_id) = (%v, %v), want (t-1, s-1)", got["trace_id"], got["span_id"])
	}
	if got["request_id"] != "req-1" || got["user_id"] != "u-1" {
		t.Errorf("request correlation lost: %v", got)
	}
}

// Without the option, or outside a span, the keys are left out and the line
// keeps today's shape.
func TestWithTraceIDs_AbsentKeepsTheLineShape(t *testing.T) {
	for name, l := range map[string]func(*bytes.Buffer) *Logger{
		"no option":  func(b *bytes.Buffer) *Logger { return New("production", b) },
		"no span":    func(b *bytes.Buffer) *Logger { return New("production", b, WithTraceIDs(fakeTraceIDs)) },
		"nil option": func(b *bytes.Buffer) *Logger { return New("production", b, WithTraceIDs(nil)) },
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			ctx := context.Background()
			if name == "no option" {
				ctx = withTrace(ctx, "t-1", "s-1")
			}
			l(&buf).InfoContext(ctx, "hello")
			got := decodeJSONLine(t, &buf)
			for _, key := range []string{"trace_id", "span_id"} {
				if v, ok := got[key]; ok {
					t.Errorf("%s = %v, want it absent", key, v)
				}
			}
		})
	}
}

// An explicit trace_id on the record or the logger wins, as request_id does.
func TestWithTraceIDs_ExplicitValueWins(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf, WithTraceIDs(fakeTraceIDs))

	l.InfoContext(withTrace(context.Background(), "t-1", "s-1"), "hello", "trace_id", "explicit")

	got := decodeJSONLine(t, &buf)
	if got["trace_id"] != "explicit" {
		t.Errorf("trace_id = %v, want explicit", got["trace_id"])
	}
	if got["span_id"] != "s-1" {
		t.Errorf("span_id = %v, want s-1", got["span_id"])
	}
}

// WithContext binds the trace IDs too, so a line logged later without ctx
// still carries them.
func TestWithTraceIDs_WithContextBindsThem(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf, WithTraceIDs(fakeTraceIDs))

	l.With("component", "x").WithContext(withTrace(requestContext("req-1", ""), "t-1", "s-1")).Info("hello")

	got := decodeJSONLine(t, &buf)
	if got["trace_id"] != "t-1" || got["span_id"] != "s-1" || got["request_id"] != "req-1" {
		t.Errorf("line = %v, want trace_id t-1, span_id s-1, request_id req-1", got)
	}
}
