package decimalx_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kafeiih/vogel/decimalx"
)

// TestParse_RejectsValuesThatHang verifies that values which parse cheaply
// but MATERIALIZE digits when operated on (hanging a worker) are rejected.
// Each case must complete quickly: if it hangs, THAT is the bug.
func TestParse_RejectsValuesThatHang(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"huge exponent 1e999999999", "1e999999999"},
		{"exponent 1e100000000", "1e100000000"},
		{"negative exponent 1e-100000000", "1e-100000000"},
		{"300-digit coefficient", strings.Repeat("9", 300)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan struct{})
			var err error
			go func() {
				_, err = decimalx.Parse(c.input)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("Parse(%q) hung (>2s): that is the bug", c.input)
			}
			if !errors.Is(err, decimalx.ErrOutOfRange) {
				t.Errorf("Parse(%q) err = %v, want ErrOutOfRange", c.input, err)
			}
		})
	}
}

// TestParse_NotParseable verifies non-numeric garbage returns
// ErrNotParseable.
func TestParse_NotParseable(t *testing.T) {
	for _, input := range []string{"abc", ""} {
		_, err := decimalx.Parse(input)
		if !errors.Is(err, decimalx.ErrNotParseable) {
			t.Errorf("Parse(%q) err = %v, want ErrNotParseable", input, err)
		}
	}
}

// TestParse_Accepted verifies legitimate amounts parse to the correct value.
func TestParse_Accepted(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"0", "0"},
		{"1000", "1000"},
		{"10.50", "10.5"},
		{"-5000", "-5000"},
		{"-0.01", "-0.01"},
		{"1e3", "1000"},
		{"1e-3", "0.001"},
		{"99999999999.99", "99999999999.99"},
		{"0.00", "0"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			d, err := decimalx.Parse(c.input)
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", c.input, err)
			}
			if d.String() != c.want {
				t.Errorf("Parse(%q) = %s, want %s", c.input, d.String(), c.want)
			}
		})
	}
}

// TestParse_ExponentLimits verifies the exponent boundary: ±8 accepted, ±9
// rejected.
func TestParse_ExponentLimits(t *testing.T) {
	accepted := []string{"1e8", "1e-8"}
	for _, s := range accepted {
		if _, err := decimalx.Parse(s); err != nil {
			t.Errorf("Parse(%q) should be accepted, err = %v", s, err)
		}
	}
	rejected := []string{"1e9", "1e-9"}
	for _, s := range rejected {
		if _, err := decimalx.Parse(s); !errors.Is(err, decimalx.ErrOutOfRange) {
			t.Errorf("Parse(%q) err = %v, want ErrOutOfRange", s, err)
		}
	}
}

// TestParse_LengthLimits verifies the length boundary: 32 chars accepted, 33
// rejected.
func TestParse_LengthLimits(t *testing.T) {
	// 32 characters, exponent 0: "1" + 31 zeros = 32 digits.
	thirtyTwo := "1" + strings.Repeat("0", 31)
	if len(thirtyTwo) != 32 {
		t.Fatalf("setup: len = %d, want 32", len(thirtyTwo))
	}
	if _, err := decimalx.Parse(thirtyTwo); err != nil {
		t.Errorf("Parse(32-char string) should be accepted, err = %v", err)
	}
	thirtyThree := "1" + strings.Repeat("0", 32)
	if len(thirtyThree) != 33 {
		t.Fatalf("setup: len = %d, want 33", len(thirtyThree))
	}
	if _, err := decimalx.Parse(thirtyThree); !errors.Is(err, decimalx.ErrOutOfRange) {
		t.Errorf("Parse(33-char string) err = %v, want ErrOutOfRange", err)
	}
}

// TestParse_LengthGuardRunsBeforeConstruction verifies the security-critical
// guard order: len(s) > maxLen must be checked BEFORE decimal.NewFromString
// is even called, so a hostile string is rejected without constructing
// anything. A 300-digit string that decimal.NewFromString could still parse
// (unlike syntactic garbage) must fail with ErrOutOfRange, not
// ErrNotParseable.
func TestParse_LengthGuardRunsBeforeConstruction(t *testing.T) {
	_, err := decimalx.Parse(strings.Repeat("9", 300))
	if !errors.Is(err, decimalx.ErrOutOfRange) {
		t.Fatalf("Parse(300 nines) err = %v, want ErrOutOfRange (length guard first)", err)
	}
	if errors.Is(err, decimalx.ErrNotParseable) {
		t.Fatalf("Parse(300 nines) err = %v, must not also be ErrNotParseable: it was rejected before parsing", err)
	}
}
