package reqctx

import (
	"context"
	"testing"
)

func TestRequestIDFromContext_NoID_ReturnsEmpty(t *testing.T) {
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Errorf("RequestIDFromContext = %q, want empty string", got)
	}
}

func TestRequestIDFromContext_RoundTrips(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-123")
	if got := RequestIDFromContext(ctx); got != "req-123" {
		t.Errorf("RequestIDFromContext = %q, want %q", got, "req-123")
	}
}

func TestRequestInfoFromContext_NoInfo_ReturnsFalse(t *testing.T) {
	info, ok := RequestInfoFromContext(context.Background())
	if ok {
		t.Errorf("expected ok=false when no RequestInfo present, got info=%+v", info)
	}
}

func TestRequestInfoFromContext_RoundTrips(t *testing.T) {
	want := RequestInfo{IP: "10.0.0.1", UserAgent: "TestAgent/1.0"}
	ctx := WithRequestInfo(context.Background(), want)

	got, ok := RequestInfoFromContext(ctx)
	if !ok {
		t.Fatal("expected ok=true after WithRequestInfo")
	}
	if got != want {
		t.Errorf("RequestInfoFromContext = %+v, want %+v", got, want)
	}
}

func TestWithRequestID_DoesNotAffectRequestInfo(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-123")
	if _, ok := RequestInfoFromContext(ctx); ok {
		t.Error("expected RequestInfo absent when only WithRequestID was called")
	}
}
