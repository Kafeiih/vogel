package decimalx_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/kafeiih/vogel/decimalx"
)

// TestNewBounds_RejectsUnsafeConfigs verifies a caller cannot switch the
// anti-DoS guard off: a zero or negative maxLen, or an inverted exponent
// range, must be refused rather than silently accepted as "unbounded".
func TestNewBounds_RejectsUnsafeConfigs(t *testing.T) {
	cases := []struct {
		name   string
		maxLen int
		minExp int32
		maxExp int32
	}{
		{"zero maxLen", 0, -8, 8},
		{"negative maxLen", -1, -8, 8},
		{"inverted exponent range", 32, 8, -8},
		{"minExp at math.MinInt32 exceeds the ceiling", 32, math.MinInt32, 8},
		{"maxExp at math.MaxInt32 exceeds the ceiling", 32, -8, math.MaxInt32},
		{"both extremes exceed the ceiling", 32, math.MinInt32, math.MaxInt32},
		{"minExp one past the ceiling", 32, -(decimalx.MaxExponentLimit + 1), 8},
		{"maxExp one past the ceiling", 32, -8, decimalx.MaxExponentLimit + 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := decimalx.NewBounds(c.maxLen, c.minExp, c.maxExp)
			if !errors.Is(err, decimalx.ErrInvalidBounds) {
				t.Fatalf("NewBounds(%d, %d, %d) err = %v, want ErrInvalidBounds", c.maxLen, c.minExp, c.maxExp, err)
			}
		})
	}
}

// TestNewBounds_AcceptsValidConfig verifies a sane override is accepted and
// actually changes behavior relative to DefaultBounds.
func TestNewBounds_AcceptsValidConfig(t *testing.T) {
	b, err := decimalx.NewBounds(4, -2, 2)
	if err != nil {
		t.Fatalf("NewBounds(4, -2, 2) err = %v, want nil", err)
	}

	if _, err := b.Parse("12.3"); err != nil {
		t.Errorf("Parse(12.3) under a 4-char bound should be accepted, err = %v", err)
	}
	if _, err := b.Parse("12345"); !errors.Is(err, decimalx.ErrOutOfRange) {
		t.Errorf("Parse(12345) is 5 chars, exceeds maxLen 4: err = %v, want ErrOutOfRange", err)
	}
	if err := b.ValidateBounds(decimal.RequireFromString("1e3")); !errors.Is(err, decimalx.ErrOutOfRange) {
		t.Errorf("ValidateBounds(1e3) exceeds maxExp 2: err = %v, want ErrOutOfRange", err)
	}
	if err := b.ValidateBounds(decimal.RequireFromString("1e2")); err != nil {
		t.Errorf("ValidateBounds(1e2) is within maxExp 2, err = %v, want nil", err)
	}
}

// TestBounds_ZeroValueIsSafe verifies the documented safety property: a
// caller who somehow ends up with a zero-value Bounds (var b
// decimalx.Bounds, or a discarded NewBounds error) still gets the DoS guard,
// not an unbounded parser. This is the load-bearing test for the package's
// "impossible or harmless to use an unbounded Bounds" requirement.
func TestBounds_ZeroValueIsSafe(t *testing.T) {
	var zero decimalx.Bounds

	// A hostile value that only a real bound would reject.
	done := make(chan error, 1)
	go func() { _, err := zero.Parse("1e100000000"); done <- err }()

	select {
	case err := <-done:
		if !errors.Is(err, decimalx.ErrOutOfRange) {
			t.Fatalf("zero-value Bounds.Parse(1e100000000) err = %v, want ErrOutOfRange (defaults must still apply)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("zero-value Bounds.Parse(1e100000000) hung: the zero value is acting as an unbounded parser")
	}

	// And it must still accept ordinary values, i.e. it behaves like
	// DefaultBounds, not like an all-rejecting bound.
	if _, err := zero.Parse("100.50"); err != nil {
		t.Errorf("zero-value Bounds.Parse(100.50) err = %v, want nil (should behave like DefaultBounds)", err)
	}

	// Same guarantee on ValidateBounds, ValidateAmount and ParseAmount.
	if err := zero.ValidateBounds(decimal.RequireFromString("1e9")); !errors.Is(err, decimalx.ErrOutOfRange) {
		t.Errorf("zero-value Bounds.ValidateBounds(1e9) err = %v, want ErrOutOfRange", err)
	}
	if err := zero.ValidateAmount(decimal.RequireFromString("1e9"), 2); !errors.Is(err, decimalx.ErrOutOfRange) {
		t.Errorf("zero-value Bounds.ValidateAmount(1e9, 2) err = %v, want ErrOutOfRange", err)
	}
	if _, err := zero.ParseAmount("1e100000000", 2); !errors.Is(err, decimalx.ErrOutOfRange) {
		t.Errorf("zero-value Bounds.ParseAmount(1e100000000, 2) err = %v, want ErrOutOfRange", err)
	}
}

// TestBounds_DiscardedNewBoundsErrorIsStillSafe verifies the exact footgun
// this design must survive: a caller ignores NewBounds' error (b, _ :=
// NewBounds(-1, 0, 0)) and uses the zero-value Bounds it gets back on the
// error path. It must still enforce DefaultBounds, never an unbounded
// parser.
func TestBounds_DiscardedNewBoundsErrorIsStillSafe(t *testing.T) {
	b, err := decimalx.NewBounds(-1, 0, 0)
	if err == nil {
		t.Fatal("setup: NewBounds(-1, 0, 0) should have failed")
	}

	if _, err := b.Parse("1e100000000"); !errors.Is(err, decimalx.ErrOutOfRange) {
		t.Fatalf("Bounds from a failed NewBounds call err = %v, want ErrOutOfRange on a hostile value", err)
	}
}

// TestDefaultBounds_MatchesPackageLevelDefaults verifies DefaultBounds
// produces exactly the bound the package-level functions (Parse,
// ValidateBounds, ValidateAmount, ParseAmount) use, so the two ways of
// reaching the default behavior cannot drift apart.
func TestDefaultBounds_MatchesPackageLevelDefaults(t *testing.T) {
	b := decimalx.DefaultBounds()

	for _, s := range []string{"1e8", "1e-8", "0", "100.50"} {
		wantD, wantErr := decimalx.Parse(s)
		gotD, gotErr := b.Parse(s)
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("Parse(%q) err = %v, DefaultBounds().Parse(%q) err = %v: diverge", s, wantErr, s, gotErr)
		}
		if wantErr == nil && !wantD.Equal(gotD) {
			t.Fatalf("Parse(%q) = %s, DefaultBounds().Parse(%q) = %s: diverge", s, wantD, s, gotD)
		}
	}

	for _, s := range []string{"1e9", "1e-9"} {
		_, wantErr := decimalx.Parse(s)
		_, gotErr := b.Parse(s)
		if !errors.Is(wantErr, decimalx.ErrOutOfRange) || !errors.Is(gotErr, decimalx.ErrOutOfRange) {
			t.Fatalf("Parse(%q): want = %v, got = %v: both should be ErrOutOfRange", s, wantErr, gotErr)
		}
	}
}

// TestNewBounds_ExponentCeiling pins MaxExponentLimit as a HARD ceiling that
// NewBounds enforces regardless of what a caller requests: an exponent range
// as wide as [math.MinInt32, math.MaxInt32] must be refused, exactly ±1000
// must be accepted, and one past that (±1001) must be refused again. Without
// this ceiling, NewBounds(32, math.MinInt32, math.MaxInt32) used to succeed
// and Bounds.Parse("1e999999999") would hang materializing the digits —
// reopening the exact DoS this package exists to close.
func TestNewBounds_ExponentCeiling(t *testing.T) {
	if _, err := decimalx.NewBounds(32, math.MinInt32, math.MaxInt32); !errors.Is(err, decimalx.ErrInvalidBounds) {
		t.Fatalf("NewBounds(32, MinInt32, MaxInt32) err = %v, want ErrInvalidBounds", err)
	}

	if _, err := decimalx.NewBounds(32, -decimalx.MaxExponentLimit, decimalx.MaxExponentLimit); err != nil {
		t.Fatalf("NewBounds(32, -%d, %d) err = %v, want nil: exactly at the ceiling must be accepted",
			decimalx.MaxExponentLimit, decimalx.MaxExponentLimit, err)
	}

	if _, err := decimalx.NewBounds(32, -(decimalx.MaxExponentLimit + 1), decimalx.MaxExponentLimit); !errors.Is(err, decimalx.ErrInvalidBounds) {
		t.Fatalf("NewBounds(32, -%d, %d) err = %v, want ErrInvalidBounds: minExp is one past the ceiling",
			decimalx.MaxExponentLimit+1, decimalx.MaxExponentLimit, err)
	}
	if _, err := decimalx.NewBounds(32, -decimalx.MaxExponentLimit, decimalx.MaxExponentLimit+1); !errors.Is(err, decimalx.ErrInvalidBounds) {
		t.Fatalf("NewBounds(32, -%d, %d) err = %v, want ErrInvalidBounds: maxExp is one past the ceiling",
			decimalx.MaxExponentLimit, decimalx.MaxExponentLimit+1, err)
	}
}

// TestNewBounds_CeilingStaysCheapToRound verifies the ceiling's own promise:
// a Bounds built at the maximum exponent range NewBounds allows still lets
// 10^1000 be parsed, bounds-checked and rounded well under the timing
// budget, so widening the range all the way to the ceiling never reopens the
// hang this package exists to prevent.
func TestNewBounds_CeilingStaysCheapToRound(t *testing.T) {
	b, err := decimalx.NewBounds(32, -decimalx.MaxExponentLimit, decimalx.MaxExponentLimit)
	if err != nil {
		t.Fatalf("NewBounds at the ceiling: err = %v, want nil", err)
	}

	done := make(chan struct{})
	go func() {
		if _, err := b.ParseAmount("1e1000", 2); err != nil {
			t.Errorf("ParseAmount(1e1000, 2) at the ceiling err = %v, want nil", err)
		}
		if err := b.ValidateAmount(decimal.New(1, 1000), 2); err != nil {
			t.Errorf("ValidateAmount(1e1000, 2) at the ceiling err = %v, want nil", err)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timingBudget):
		t.Fatal("ParseAmount/ValidateAmount at the exponent ceiling did not return within the timing budget")
	}
}
