package cerbos

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// closedAddr returns a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// hangupAddr returns a loopback address that accepts connections and closes
// them at once, like a PDP that does not speak the transport the client
// chose (TLS against a plaintext listener, or the reverse).
func hangupAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().String()
}

func TestChecker_Ping_UnreachablePDP_ReturnsErrorNamingHost(t *testing.T) {
	tests := []struct {
		name      string
		addr      func(*testing.T) string
		plaintext bool
	}{
		{name: "nothing listening, plaintext", addr: closedAddr, plaintext: true},
		{name: "nothing listening, TLS", addr: closedAddr},
		{name: "listener hangs up, plaintext", addr: hangupAddr, plaintext: true},
		{name: "listener hangs up, TLS", addr: hangupAddr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := tt.addr(t)
			c, err := New(Config{Host: addr, Plaintext: tt.plaintext})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			start := time.Now()
			err = c.Ping(ctx)
			if err == nil {
				t.Fatal("Ping = nil against an unreachable PDP, want an error")
			}
			if !strings.Contains(err.Error(), addr) {
				t.Errorf("Ping error %q does not name the host %s", err, addr)
			}
			if elapsed := time.Since(start); elapsed > 4*time.Second {
				t.Errorf("Ping took %v, want it to fail fast instead of waiting for the context", elapsed)
			}
		})
	}
}
