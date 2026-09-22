package decimalx_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/kafeiih/vogel/decimalx"
)

// =============================================================================
// ValidateBounds — the same bound Parse applies, over a decimal that is
// already CONSTRUCTED.
//
// Parse only guards the string door. Not every decimal arrives through a
// string: a bulk-load payload with a field typed decimal.Decimal is built by
// encoding/json directly, and Parse never sees it. That path needs the SAME
// bound, so it lives in one function Parse also calls — if the bound were
// duplicated, one copy would eventually drift.
//
// These tests deliberately never materialize the digits (no String(),
// Round(), or comparisons on the hostile value): materializing
// "1e100000000" is exactly what hangs a worker, and it would hang the test
// too. They assert that the bound REJECTS the value, never that the value
// "equals" anything.
// =============================================================================

// TestValidateBounds_RejectsAlreadyConstructedDecimal covers the real gap:
// the decimal arrives already built (as encoding/json leaves it), not
// parsed from a string by us.
func TestValidateBounds_RejectsAlreadyConstructedDecimal(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"huge exponent 1e999999999", "1e999999999"},
		{"exponent 1e100000000", "1e100000000"},
		{"negative exponent 1e-100000000", "1e-100000000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// RequireFromString is lazy: it stores (coefficient, exponent)
			// without materializing. Building the hostile value is cheap;
			// operating on it would be expensive, and we never do that.
			d := decimal.RequireFromString(c.input)

			// The bound must be O(1): it reads the exponent field, it never
			// expands the digits. If this hangs, the implementation is
			// materializing and the DoS is still open.
			done := make(chan error, 1)
			go func() { done <- decimalx.ValidateBounds(d) }()

			select {
			case err := <-done:
				if !errors.Is(err, decimalx.ErrOutOfRange) {
					t.Errorf("ValidateBounds(%s) err = %v, want ErrOutOfRange", c.input, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("ValidateBounds(%s) hung (>2s): it materialized the digits, THAT is the bug", c.input)
			}
		})
	}
}

// TestValidateBounds_ExponentLimits pins the boundary: ±8 in, ±9 out. Same
// boundary TestParse_ExponentLimits already verifies, now over the
// constructed decimal.
func TestValidateBounds_ExponentLimits(t *testing.T) {
	for _, s := range []string{"1e8", "1e-8"} {
		if err := decimalx.ValidateBounds(decimal.RequireFromString(s)); err != nil {
			t.Errorf("ValidateBounds(%s) should be accepted, err = %v", s, err)
		}
	}
	for _, s := range []string{"1e9", "1e-9"} {
		if err := decimalx.ValidateBounds(decimal.RequireFromString(s)); !errors.Is(err, decimalx.ErrOutOfRange) {
			t.Errorf("ValidateBounds(%s) err = %v, want ErrOutOfRange", s, err)
		}
	}
}

// TestValidateBounds_LongCoefficient verifies the other bound Parse applies
// — the coefficient one — expressed over the decimal: a coefficient longer
// than 32 digits is rejected.
func TestValidateBounds_LongCoefficient(t *testing.T) {
	// 33 digits: exceeds maxLen by one.
	long := decimal.RequireFromString(strings.Repeat("9", 33))
	if err := decimalx.ValidateBounds(long); !errors.Is(err, decimalx.ErrOutOfRange) {
		t.Errorf("ValidateBounds(33-digit coefficient) err = %v, want ErrOutOfRange", err)
	}
	// 32 digits: exactly at the bound, accepted.
	exact := decimal.RequireFromString(strings.Repeat("9", 32))
	if err := decimalx.ValidateBounds(exact); err != nil {
		t.Errorf("ValidateBounds(32-digit coefficient) should be accepted, err = %v", err)
	}
}

// TestValidateBounds_AcceptsLegitimateAmounts guards against over-rejection.
// The bound is anti-DoS only: it is NOT a money rule. "100.005" has 3
// decimals and is ACCEPTED here on purpose — rounding or rejecting it is a
// different rule, with its own function (ValidateAmount).
func TestValidateBounds_AcceptsLegitimateAmounts(t *testing.T) {
	for _, s := range []string{"0", "1500000.00", "100.005", "-5000", "99999999999.99", "0.01"} {
		if err := decimalx.ValidateBounds(decimal.RequireFromString(s)); err != nil {
			t.Errorf("ValidateBounds(%s) should be accepted, err = %v", s, err)
		}
	}
}

// TestValidateBounds_ZeroDecimal verifies the zero value (decimal.Decimal{}),
// which is what encoding/json leaves when the amount field is absent or
// null: it must pass the bound, not explode.
func TestValidateBounds_ZeroDecimal(t *testing.T) {
	var zero decimal.Decimal
	if err := decimalx.ValidateBounds(zero); err != nil {
		t.Errorf("ValidateBounds(decimal.Decimal{}) should be accepted, err = %v", err)
	}
}

// TestParse_StringDoorIsStricterThanValidateBoundsDigitDoor pins the one
// documented, intentional divergence between Parse and ValidateBounds:
// Parse's pre-check bounds the STRING's length (including a leading sign or
// a decimal point), while ValidateBounds' digit bound looks only at the
// COEFFICIENT's digit count (NumDigits, which never counts the sign). A
// 32-digit value written with a leading "-" is 33 characters, one past
// MaxLen, so Parse rejects it with ErrOutOfRange even though the exact same
// VALUE, decoded without going through the string door (as encoding/json
// would build it), has a 32-digit coefficient and is squarely within
// ValidateBounds' bound. This makes the string door STRICTER than the digit
// door, never looser: nothing ValidateBounds rejects is accepted by Parse.
func TestParse_StringDoorIsStricterThanValidateBoundsDigitDoor(t *testing.T) {
	digits := strings.Repeat("9", 32)
	negative := "-" + digits // 33 characters: sign + 32 digits.

	if len(negative) != 33 {
		t.Fatalf("setup: len(%q) = %d, want 33", negative, len(negative))
	}

	if _, err := decimalx.Parse(negative); !errors.Is(err, decimalx.ErrOutOfRange) {
		t.Fatalf("Parse(%q) err = %v, want ErrOutOfRange: 33 characters exceeds Parse's MaxLen of 32", negative, err)
	}

	// Build the identical value without going through Parse's string door:
	// a 32-digit unsigned coefficient, negated. NumDigits ignores the sign,
	// so this decimal's coefficient is still exactly 32 digits.
	unsigned, err := decimalx.Parse(digits)
	if err != nil {
		t.Fatalf("Parse(%q) err = %v, want nil", digits, err)
	}
	d := unsigned.Neg()

	if err := decimalx.ValidateBounds(d); err != nil {
		t.Errorf("ValidateBounds(%s) err = %v, want nil: the string door is stricter than the digit door, never looser", d, err)
	}
}

// TestParse_DelegatesToValidateBounds anchors the invariant that prevents
// drift: Parse and ValidateBounds must agree on EVERY value Parse manages to
// build. If either bound is relaxed on only one side, this test catches it.
func TestParse_DelegatesToValidateBounds(t *testing.T) {
	for _, s := range []string{"0", "1000", "10.50", "-0.01", "1e8", "1e-8", "1e9", "1e-9", "100.005"} {
		d, errParse := decimalx.Parse(s)
		if errParse != nil {
			// Parse rejected: ValidateBounds must reject the same value
			// built separately.
			if !errors.Is(errParse, decimalx.ErrOutOfRange) {
				continue // not parseable: out of ValidateBounds' contract.
			}
			if err := decimalx.ValidateBounds(decimal.RequireFromString(s)); !errors.Is(err, decimalx.ErrOutOfRange) {
				t.Errorf("drift: Parse(%s) rejects but ValidateBounds accepts (err = %v)", s, err)
			}
			continue
		}
		// Parse accepted: ValidateBounds must accept the decimal Parse
		// returned.
		if err := decimalx.ValidateBounds(d); err != nil {
			t.Errorf("drift: Parse(%s) accepts but ValidateBounds rejects (err = %v)", s, err)
		}
	}
}
