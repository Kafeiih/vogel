package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

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
