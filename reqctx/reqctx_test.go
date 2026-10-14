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

func TestClientRequestIDFromContext_NoID_ReturnsEmpty(t *testing.T) {
	if got := ClientRequestIDFromContext(context.Background()); got != "" {
		t.Errorf("ClientRequestIDFromContext = %q, want empty string", got)
	}
}

func TestClientRequestID_IsSeparateFromRequestID(t *testing.T) {
	ctx := WithClientRequestID(WithRequestID(context.Background(), "server-1"), "client-1")
	if got := ClientRequestIDFromContext(ctx); got != "client-1" {
		t.Errorf("ClientRequestIDFromContext = %q, want %q", got, "client-1")
	}
	if got := RequestIDFromContext(ctx); got != "server-1" {
		t.Errorf("RequestIDFromContext = %q, want %q (the client ID must never replace it)", got, "server-1")
	}
}

func TestUserIDFromContext_NoSlot_ReturnsEmpty(t *testing.T) {
	if got := UserIDFromContext(context.Background()); got != "" {
		t.Errorf("UserIDFromContext = %q, want empty string", got)
	}
}

// Authenticate may be mounted without RequestContext upstream, so there is no
// slot to fill: SetUserID must then do nothing rather than panic.
func TestSetUserID_NoSlot_IsANoOp(t *testing.T) {
	ctx := context.Background()
	SetUserID(ctx, "u-1")
	if got := UserIDFromContext(ctx); got != "" {
		t.Errorf("UserIDFromContext = %q, want empty string without a slot", got)
	}
}

func TestWithUserSlot_EmptyUntilSet(t *testing.T) {
	ctx := WithUserSlot(context.Background())
	if got := UserIDFromContext(ctx); got != "" {
		t.Errorf("UserIDFromContext = %q, want empty string before SetUserID", got)
	}
}

// The slot exists so that a value written through a context derived further
// down the chain is visible through the ancestor context an outer middleware
// holds — the whole reason it is a slot rather than a plain context value.
func TestUserSlot_WriteThroughDerivedContextIsVisibleUpstream(t *testing.T) {
	type otherKey struct{}
	outer := WithUserSlot(context.Background())
	inner := context.WithValue(WithRequestID(outer, "req-1"), otherKey{}, "x")

	SetUserID(inner, "u-42")

	if got := UserIDFromContext(outer); got != "u-42" {
		t.Errorf("UserIDFromContext(outer) = %q, want %q", got, "u-42")
	}
	if got := UserIDFromContext(inner); got != "u-42" {
		t.Errorf("UserIDFromContext(inner) = %q, want %q", got, "u-42")
	}
}

// A second WithUserSlot on a context that already carries one (e.g.
// RequestContext mounted twice) must reuse it: a fresh inner slot would
// shadow the outer one, and a middleware holding the outer context would
// never see the user ID written through the inner one.
func TestWithUserSlot_ReusesExistingSlot(t *testing.T) {
	outer := WithUserSlot(context.Background())
	inner := WithUserSlot(outer)

	SetUserID(inner, "u-7")

	if got := UserIDFromContext(outer); got != "u-7" {
		t.Errorf("UserIDFromContext(outer) = %q, want %q", got, "u-7")
	}
}
