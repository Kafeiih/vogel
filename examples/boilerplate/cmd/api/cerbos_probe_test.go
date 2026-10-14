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
	failFirst   int // when > 0, the first failFirst calls return err and later ones succeed
	calls       int
	hadDeadline bool
	deadline    time.Duration
}

func (f *fakePinger) Ping(ctx context.Context) error {
	f.calls++
	if d, ok := ctx.Deadline(); ok {
		f.hadDeadline = true
		f.deadline = time.Until(d)
	}
	if f.failFirst > 0 {
		if f.calls <= f.failFirst {
			return f.err
		}
		return nil
	}
	return f.err
}

// fastProbe shrinks the retry budget and backoff so tests of the failure
// paths run in milliseconds instead of the production 30s.
func fastProbe(t *testing.T) {
	t.Helper()
	budget, backoff := cerbosProbeBudget, cerbosProbeBackoff
	cerbosProbeBudget, cerbosProbeBackoff = 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { cerbosProbeBudget, cerbosProbeBackoff = budget, backoff })
}

// A PDP that is briefly unavailable at boot (container start order, a Cerbos
// rollout) must not kill the API: the probe retries until it answers.
func TestProbeCerbos_RetriesUntilReachable(t *testing.T) {
	fastProbe(t)
	p := &fakePinger{err: errors.New("connection refused"), failFirst: 2}
	if err := probeCerbos(context.Background(), p, cerbos.Config{Host: "cerbos:3593"}); err != nil {
		t.Fatalf("probeCerbos = %v, want nil once the PDP answers", err)
	}
	if p.calls != 3 {
		t.Errorf("Ping calls = %d, want 3 (two failures, then success)", p.calls)
	}
}

// A PDP that never answers still fails the boot, once the budget is spent.
func TestProbeCerbos_GivesUpAfterBudget(t *testing.T) {
	fastProbe(t)
	p := &fakePinger{err: errors.New("connection refused")}
	start := time.Now()
	if err := probeCerbos(context.Background(), p, cerbos.Config{Host: "cerbos:3593"}); err == nil {
		t.Fatal("probeCerbos = nil, want an error after the retry budget")
	}
	if p.calls < 2 {
		t.Errorf("Ping calls = %d, want at least 2 (it must retry)", p.calls)
	}
	if elapsed := time.Since(start); elapsed > cerbosProbeBudget+cerbosProbeTimeout {
		t.Errorf("probe took %v, want within the budget %v plus one attempt", elapsed, cerbosProbeBudget)
	}
}

func TestProbeCerbos(t *testing.T) {
	fastProbe(t)
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
	fastProbe(t)
	cfg := cerbos.Config{Host: "127.0.0.1:1", Plaintext: true}
	checker, err := cerbos.New(cfg)
	if err != nil {
		t.Fatalf("cerbos.New: %v", err)
	}
	start := time.Now()
	if err := probeCerbos(context.Background(), checker, cfg); err == nil {
		t.Fatal("probeCerbos = nil against a closed port, want an error")
	}
	if elapsed := time.Since(start); elapsed >= cerbosProbeBudget+cerbosProbeTimeout {
		t.Errorf("probe took %v, want under the budget %v plus one attempt", elapsed, cerbosProbeBudget)
	}
}
