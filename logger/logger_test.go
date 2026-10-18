package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
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

// NewDefault is New over os.Stdout, so it inherits the UTC conversion; this
// pins that, so a NewDefault that ever built its own handler options could
// not silently drop it. It swaps os.Stdout, so it must not run in parallel.
func TestNewDefault_TimestampsAreUTC(t *testing.T) {
	withLocalZone(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = prev })

	NewDefault("production").Info("hello")
	_ = w.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if ts, _ := decoded["time"].(string); !strings.HasSuffix(ts, "Z") {
		t.Errorf("time = %q, want a UTC timestamp ending in Z", ts)
	}
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

// decodeJSONLine decodes a single JSON log line, failing the test otherwise.
func decodeJSONLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	return decoded
}

// requestContext returns a context carrying the request ID and an
// authenticated user, as httpx/middleware leaves it for a handler.
func requestContext(requestID, userID string) context.Context {
	ctx := reqctx.WithRequestID(context.Background(), requestID)
	ctx = reqctx.WithUserSlot(ctx)
	reqctx.SetUserID(ctx, userID)
	return ctx
}

// The *Context methods read reqctx themselves, so a caller that passes ctx
// gets the request correlation without remembering to call WithContext.
func TestInfoContext_AddsRequestIDAndUserIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)

	l.InfoContext(requestContext("req-1", "u-1"), "hello")

	decoded := decodeJSONLine(t, &buf)
	if decoded["request_id"] != "req-1" {
		t.Errorf("request_id = %v, want %q", decoded["request_id"], "req-1")
	}
	if decoded["user_id"] != "u-1" {
		t.Errorf("user_id = %v, want %q", decoded["user_id"], "u-1")
	}
}

// slog.Default() set to the Logger's handler gets the same correlation, so
// package-level slog.InfoContext calls are covered too.
func TestInfoContext_ThroughSlogDefault_AddsRequestID(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)

	prev := slog.Default()
	slog.SetDefault(l.Logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	slog.InfoContext(requestContext("req-2", "u-2"), "hello")

	if decoded := decodeJSONLine(t, &buf); decoded["request_id"] != "req-2" {
		t.Errorf("request_id = %v, want %q", decoded["request_id"], "req-2")
	}
}

// Outside a request the keys are left out, so the line keeps its shape.
func TestInfoContext_NoRequestValues_OmitsKeys(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)

	l.InfoContext(context.Background(), "hello")

	decoded := decodeJSONLine(t, &buf)
	for _, key := range []string{"request_id", "user_id"} {
		if v, ok := decoded[key]; ok {
			t.Errorf("%s = %v, want the key absent", key, v)
		}
	}
}

// A Logger already bound with WithContext must not print the keys twice
// when it also logs with a context.
func TestInfoContext_AfterWithContext_LogsKeysOnce(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)
	ctx := requestContext("req-3", "u-3")

	l.WithContext(ctx).InfoContext(ctx, "hello")

	for _, key := range []string{`"request_id"`, `"user_id"`} {
		if n := strings.Count(buf.String(), key); n != 1 {
			t.Errorf("%s appears %d times in %q, want 1", key, n, buf.String())
		}
	}
}

// A value the caller bound explicitly wins over the one in ctx.
func TestInfoContext_ExplicitRequestID_WinsOverContext(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)

	l.With("request_id", "explicit").InfoContext(requestContext("from-ctx", "u-4"), "hello")

	if n := strings.Count(buf.String(), `"request_id"`); n != 1 {
		t.Fatalf("request_id appears %d times in %q, want 1", n, buf.String())
	}
	if decoded := decodeJSONLine(t, &buf); decoded["request_id"] != "explicit" {
		t.Errorf("request_id = %v, want %q", decoded["request_id"], "explicit")
	}
}

// Callers such as httpx/middleware's auth and access logs pass request_id
// and user_id as record attributes themselves; those must not be doubled.
func TestInfoContext_RecordAttrs_WinOverContext(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)

	l.InfoContext(requestContext("from-ctx", "u-ctx"), "hello",
		"request_id", "explicit", "user_id", "u-explicit")

	for _, key := range []string{`"request_id"`, `"user_id"`} {
		if n := strings.Count(buf.String(), key); n != 1 {
			t.Errorf("%s appears %d times in %q, want 1", key, n, buf.String())
		}
	}
	if decoded := decodeJSONLine(t, &buf); decoded["request_id"] != "explicit" {
		t.Errorf("request_id = %v, want %q", decoded["request_id"], "explicit")
	}
}

// Inside a group, record attributes land in the group, so keys bound after
// WithGroup are the ones that collide; they must not be doubled either.
func TestInfoContext_InsideGroup_LogsKeysOnce(t *testing.T) {
	ctx := requestContext("from-ctx", "u-ctx")

	for name, build := range map[string]func(*Logger) *slog.Logger{
		"WithContext after group": func(l *Logger) *slog.Logger {
			return (&Logger{Logger: l.WithGroup("g")}).WithContext(ctx).Logger
		},
		"With after group": func(l *Logger) *slog.Logger {
			return l.WithGroup("g").With("request_id", "explicit", "user_id", "u-explicit")
		},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			build(New("production", &buf)).InfoContext(ctx, "hello")

			for _, key := range []string{`"request_id"`, `"user_id"`} {
				if n := strings.Count(buf.String(), key); n != 1 {
					t.Errorf("%s appears %d times in %q, want 1", key, n, buf.String())
				}
			}
		})
	}
}

// A group opened after the keys were bound at the top level still gets the
// context values inside it: the top-level ones do not collide with them.
func TestInfoContext_GroupAfterTopLevelBinding_AddsKeysInsideGroup(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)
	ctx := requestContext("req-g", "u-g")

	l.WithContext(ctx).WithGroup("g").InfoContext(ctx, "hello")

	decoded := decodeJSONLine(t, &buf)
	if decoded["request_id"] != "req-g" {
		t.Errorf("top-level request_id = %v, want %q", decoded["request_id"], "req-g")
	}
	group, _ := decoded["g"].(map[string]any)
	if group["request_id"] != "req-g" {
		t.Errorf("g.request_id = %v, want %q", group["request_id"], "req-g")
	}
}
