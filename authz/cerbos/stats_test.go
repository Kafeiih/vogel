package cerbos

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/stats"

	"github.com/kafeiih/vogel/authz"
)

// countingHandler counts the RPCs gRPC reports to it.
type countingHandler struct{ rpcs atomic.Int32 }

func (h *countingHandler) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	h.rpcs.Add(1)
	return ctx
}
func (h *countingHandler) HandleRPC(context.Context, stats.RPCStats) {}
func (h *countingHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}
func (h *countingHandler) HandleConn(context.Context, stats.ConnStats) {}

// Config.StatsHandler sees every call the Checker makes to the PDP, even one
// that fails because nothing answers.
func TestConfig_StatsHandlerSeesEveryCall(t *testing.T) {
	h := &countingHandler{}
	c, err := New(Config{Host: "127.0.0.1:1", Plaintext: true, CallTimeout: 200 * time.Millisecond, StatsHandler: h})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_, _ = c.IsAllowed(context.Background(), authz.Principal{ID: "u1", Roles: []string{"user"}}, authz.Resource{Kind: "document", ID: "d1"}, "read")

	if h.rpcs.Load() == 0 {
		t.Error("StatsHandler saw no RPC, want the CheckResources call")
	}
}
