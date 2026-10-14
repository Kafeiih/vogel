package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kafeiih/vogel/authz/cerbos"
)

type fakePinger struct {
	err         error
	hadDeadline bool
	deadline    time.Duration
}

func (f *fakePinger) Ping(ctx context.Context) error {
	if d, ok := ctx.Deadline(); ok {
		f.hadDeadline = true
		f.deadline = time.Until(d)
	}
	return f.err
}

func TestProbeCerbos(t *testing.T) {
	pingErr := errors.New("connection refused")
	tests := []struct {
		name      string
		err       error
		plaintext bool
		wantMode  string
	}{
		{name: "reachable PDP", err: nil},
		{name: "unreachable PDP over TLS", err: pingErr, wantMode: "TLS"},
		{name: "unreachable PDP over plaintext", err: pingErr, plaintext: true, wantMode: "plaintext"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakePinger{err: tt.err}
			err := probeCerbos(context.Background(), p, cerbos.Config{Host: "cerbos:3593", Plaintext: tt.plaintext})
			if !p.hadDeadline || p.deadline > cerbosProbeTimeout {
				t.Errorf("probe context deadline = %v (set %v), want at most %v", p.deadline, p.hadDeadline, cerbosProbeTimeout)
			}
			if tt.err == nil {
				if err != nil {
					t.Fatalf("probeCerbos = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.err) {
				t.Errorf("probeCerbos = %v, want wrapping %v", err, tt.err)
			}
			for _, want := range []string{"cerbos:3593", "CERBOS_PLAINTEXT", tt.wantMode} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("probeCerbos error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// TestProbeCerbos_RealCheckerAgainstClosedPort wires the real adapter: a PDP
// address nothing listens on fails the probe well before the timeout.
func TestProbeCerbos_RealCheckerAgainstClosedPort(t *testing.T) {
	cfg := cerbos.Config{Host: "127.0.0.1:1", Plaintext: true}
	checker, err := cerbos.New(cfg)
	if err != nil {
		t.Fatalf("cerbos.New: %v", err)
	}
	start := time.Now()
	if err := probeCerbos(context.Background(), checker, cfg); err == nil {
		t.Fatal("probeCerbos = nil against a closed port, want an error")
	}
	if elapsed := time.Since(start); elapsed >= cerbosProbeTimeout {
		t.Errorf("probe took %v, want under %v", elapsed, cerbosProbeTimeout)
	}
}
