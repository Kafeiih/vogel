package cerbos

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/kafeiih/vogel/authz"
)

// silentAddr returns a loopback address that accepts connections and holds
// them open without ever reading or writing, like a PDP that is up at the
// TCP level but wedged: the gRPC handshake never completes and no RPC is
// ever answered.
func silentAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	return ln.Addr().String()
}

func TestNew_NegativeCallTimeout_ReturnsError(t *testing.T) {
	if _, err := New(Config{Host: "localhost:3593", CallTimeout: -time.Second}); err == nil {
		t.Error("New with a negative CallTimeout = nil error, want an error")
	}
}

func TestNew_CallTimeoutDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "zero uses the default", in: 0, want: DefaultCallTimeout},
		{name: "explicit value is kept", in: 750 * time.Millisecond, want: 750 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(Config{Host: "localhost:3593", CallTimeout: tt.in})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if c.callTimeout != tt.want {
				t.Errorf("callTimeout = %v, want %v", c.callTimeout, tt.want)
			}
		})
	}
}

// A PDP that accepts the connection and never answers must not hang the
// request when the caller's context has no deadline: every call is bounded
// by Config.CallTimeout and fails with an error (which the access layer maps
// to 503, like any other checker error).
func TestChecker_SilentPDP_CallsAreBoundedByCallTimeout(t *testing.T) {
	const callTimeout = 300 * time.Millisecond
	calls := []struct {
		name string
		call func(*Checker) error
	}{
		{name: "IsAllowed", call: func(c *Checker) error {
			_, err := c.IsAllowed(context.Background(),
				authz.Principal{ID: "u1", Roles: []string{"user"}},
				authz.Resource{Kind: "document", ID: "d1"}, "read")
			return err
		}},
		{name: "Ping", call: func(c *Checker) error { return c.Ping(context.Background()) }},
	}
	for _, tt := range calls {
		for _, plaintext := range []bool{true, false} {
			name := tt.name + "/TLS"
			if plaintext {
				name = tt.name + "/plaintext"
			}
			t.Run(name, func(t *testing.T) {
				c, err := New(Config{Host: silentAddr(t), Plaintext: plaintext, CallTimeout: callTimeout})
				if err != nil {
					t.Fatalf("New: %v", err)
				}

				done := make(chan error, 1)
				start := time.Now()
				go func() { done <- tt.call(c) }()

				select {
				case err := <-done:
					if err == nil {
						t.Fatal("call against a silent PDP = nil error, want an error")
					}
					if elapsed := time.Since(start); elapsed > 3*time.Second {
						t.Errorf("call took %v, want it bounded near CallTimeout %v", elapsed, callTimeout)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("call still blocked after 5s with CallTimeout %v: the PDP call has no deadline of its own", callTimeout)
				}
			})
		}
	}
}

// A caller deadline shorter than CallTimeout still wins: the timeout only
// adds a bound, it never extends one.
func TestChecker_CallerDeadlineShorterThanCallTimeout_Wins(t *testing.T) {
	c, err := New(Config{Host: silentAddr(t), Plaintext: true, CallTimeout: time.Minute})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := c.Ping(ctx); err == nil {
		t.Fatal("Ping = nil against a silent PDP, want an error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Ping took %v, want the caller's 200ms deadline to win", elapsed)
	}
}
