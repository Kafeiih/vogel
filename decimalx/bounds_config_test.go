package decimalx_test

import (
	"errors"
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
