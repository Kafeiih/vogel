package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kafeiih/vogel/reqctx"
)

func TestNew_DevelopmentUsesTextHandler(t *testing.T) {
	var buf bytes.Buffer
	l := New("development", &buf)
	l.Info("hello")

	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("expected text output to contain %q, got %q", "hello", buf.String())
	}
}

func TestNew_ProductionUsesJSONHandler(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)
	l.Info("hello")

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("expected JSON output, got %q: %v", buf.String(), err)
	}
	if decoded["msg"] != "hello" {
		t.Errorf("msg = %v, want %q", decoded["msg"], "hello")
	}
}

func TestWith_AddsAttributes(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)
	l2 := l.With("component", "test")
	l2.Info("hello")

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["component"] != "test" {
		t.Errorf("component = %v, want %q", decoded["component"], "test")
	}
}

func TestWithContext_NoRequestID_UsesEmptyString(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)
	l.WithContext(context.Background()).Info("hello")

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["request_id"] != "" {
		t.Errorf("request_id = %v, want empty string", decoded["request_id"])
	}
}

func TestWithContext_RequestIDPresent_IsLogged(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)

	ctx := reqctx.WithRequestID(context.Background(), "req-123")
	l.WithContext(ctx).Info("hello")

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["request_id"] != "req-123" {
		t.Errorf("request_id = %v, want %q", decoded["request_id"], "req-123")
	}
}

func TestWithContext_UsesReqctxAsTheOwningPackage(t *testing.T) {
	// Regression test for the logger/reqctx consolidation: the request ID
	// Logger.WithContext logs must come from reqctx's context key, the same
	// one audit.Recorder.Record reads, so a log line and an audit_log row for
	// the same request are provably linked by the same value.
	var buf bytes.Buffer
	l := New("production", &buf)

	ctx := reqctx.WithRequestID(context.Background(), "abc")
	l.WithContext(ctx).Info("hello")

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["request_id"] != "abc" {
		t.Errorf("request_id = %v, want %q", decoded["request_id"], "abc")
	}
}

func TestNewDefault_DoesNotPanic(t *testing.T) {
	// NewDefault writes to os.Stdout; we only verify construction doesn't panic
	// and produces a usable Logger.
	l := NewDefault("development")
	if l == nil {
		t.Fatal("expected non-nil logger")
	}
	if l.Logger == nil {
		t.Fatal("expected non-nil underlying slog.Logger")
	}
}

// withLocalZone runs the test with time.Local set to a fixed non-UTC zone, so
// a timestamp left in local time is distinguishable from one in UTC.
func withLocalZone(t *testing.T) {
	t.Helper()
	prev := time.Local
	time.Local = time.FixedZone("UTC+3", 3*60*60)
	t.Cleanup(func() { time.Local = prev })
}

// Log timestamps are emitted in UTC whatever the host's zone, so lines from
// services in different zones sort and correlate without offset arithmetic.
func TestNew_TimestampsAreUTC(t *testing.T) {
	withLocalZone(t)

	t.Run("production JSON", func(t *testing.T) {
		var buf bytes.Buffer
		New("production", &buf).Info("hello")

		var decoded map[string]any
		if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		ts, _ := decoded["time"].(string)
		if !strings.HasSuffix(ts, "Z") {
			t.Errorf("time = %q, want a UTC timestamp ending in Z", ts)
		}
	})

	t.Run("development text", func(t *testing.T) {
		var buf bytes.Buffer
		New("development", &buf).Info("hello")

		field := strings.Fields(buf.String())[0]
		if !strings.HasPrefix(field, "time=") || !strings.HasSuffix(field, "Z") {
			t.Errorf("first field = %q, want time=<UTC timestamp ending in Z>", field)
		}
	})
}

// Only the record's own timestamp is converted; a time-valued attribute the
// caller logs keeps its zone.
func TestNew_UTCConversionLeavesOtherTimeAttrsAlone(t *testing.T) {
	withLocalZone(t)

	var buf bytes.Buffer
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	New("production", &buf).Info("hello", "at", at)

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["at"] != "2026-01-02T03:04:05+03:00" {
		t.Errorf("at = %v, want the caller's zone preserved", decoded["at"])
	}
}

func TestWithContext_UserIDFromSlot_IsLogged(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)

	ctx := reqctx.WithUserSlot(context.Background())
	reqctx.SetUserID(ctx, "u-42")
	l.WithContext(ctx).Info("hello")

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["user_id"] != "u-42" {
		t.Errorf("user_id = %v, want %q", decoded["user_id"], "u-42")
	}
}

// Without an authenticated user the key is left out, so existing log lines
// keep their shape.
func TestWithContext_NoUserID_OmitsKey(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)
	l.WithContext(reqctx.WithUserSlot(context.Background())).Info("hello")

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v, ok := decoded["user_id"]; ok {
		t.Errorf("user_id = %v, want the key absent", v)
	}
}
