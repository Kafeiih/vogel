package decimalx_test

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/kafeiih/vogel/decimalx"
)

// TestParseAmount_MaxTwoDecimals pins THE MONEY RULE at scale 2: a monetary
// column is scale 2 and rounds silently on write, while validation runs in
// memory at the PARSED precision. Accepting a third decimal would make "what
// was validated" and "what was persisted" stop being the same number. It is
// REJECTED, NOT ROUNDED: rounding silently loses the client money without
// telling them.
//
// THE CRITERION IS d != round(d, scale), NOT exp < -scale. Exponent() is the
// literal's scale, not the value's real precision: "100.500" has exponent -3
// but is worth exactly 100.50, and the column stores it without losing a
// cent. Rejecting it would punish a client who pads with zeros. Only a value
// that CHANGES when quantized to scale decimals is rejected — that is the
// only case where money evaporates.
func TestParseAmount_MaxTwoDecimals(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr error // nil => accepted
	}{
		{"integer, no decimals", "100", nil},
		{"one decimal", "100.5", nil},
		{"exactly two decimals", "100.50", nil},
		// The edge: exponent -3, but worth 100.50 EXACTLY. The DB writes it
		// without losing a cent, so it is accepted. Rejecting it would be a
		// false positive against a client who pads with zeros.
		{"three decimals but exact at scale 2 (padding zeros)", "100.500", nil},
		{"many padding zeros", "100.5000", nil},
		{"zero", "0", nil},
		{"negative with two decimals", "-100.50", nil},

		{"three significant decimals", "100.005", decimalx.ErrTooManyDecimals},
		{"third decimal the DB would round away", "33.334", decimalx.ErrTooManyDecimals},
		{"negative with a third decimal", "-33.334", decimalx.ErrTooManyDecimals},
		{"third decimal on a near-zero value", "0.004", decimalx.ErrTooManyDecimals},
		{"eight decimals (Parse's ceiling, still invalid money)", "0.00000001", decimalx.ErrTooManyDecimals},

		{"not parseable", "not-a-number", decimalx.ErrNotParseable},
		{"empty", "", decimalx.ErrNotParseable},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decimalx.ParseAmount(c.input, 2)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("ParseAmount(%q, 2) err = %v, want %v", c.input, err, c.wantErr)
				}
				if !got.IsZero() {
					t.Fatalf("ParseAmount(%q, 2) = %s, want zero on the error path", c.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAmount(%q, 2) err = %v, want nil", c.input, err)
			}
			if want := decimal.RequireFromString(c.input); !got.Equal(want) {
				t.Fatalf("ParseAmount(%q, 2) = %s, want %s", c.input, got, want)
			}
			// What is accepted must be exactly what the column will store.
			if !got.Equal(got.Round(2)) {
				t.Fatalf("ParseAmount(%q, 2) = %s: does not survive the column's round(2)", c.input, got)
			}
		})
	}
}

// TestParseAmount_AppliesBoundsBeforeRounding is the anti-drift test: it
// fails if ParseAmount stops delegating to Parse and is left only with the
// 2-decimal rule.
//
// Order matters and is what is pinned here. shopspring/decimal stores
// (coefficient, exponent) lazily: "1e100000000" parses in microseconds, but
// Round(2) MATERIALIZES the digits and hangs the worker with a ~20-byte
// payload. If the bound did not run FIRST, this very test would hang.
//
// That is why the huge-exponent case demands ErrOutOfRange — the BOUND
// rejects it, not the money rule — and the test NEVER materializes the
// value: it never compares it, prints it, or serializes it. It only looks at
// the error. The coefficient-length bound is in the same table because it is
// the other half of what Parse contributes.
func TestParseAmount_AppliesBoundsBeforeRounding(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"huge exponent (the one that hangs on materialization)", "1e100000000"},
		{"huge negative exponent", "1e-100000000"},
		{"exponent barely out of bounds", "1e9"},
		{"coefficient longer than the bound", "1234567890123456789012345678901234567890"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Never compare or print the returned value: materializing it
			// is exactly the DoS the bound exists to stop.
			_, err := decimalx.ParseAmount(c.input, 2)
			if !errors.Is(err, decimalx.ErrOutOfRange) {
				t.Fatalf("ParseAmount(%q, 2) err = %v, want ErrOutOfRange (the bound, not the money rule)", c.input, err)
			}
		})
	}
}

// TestParseAmount_InheritsParseContract closes the anti-drift loop from the
// other side: everything Parse rejects, ParseAmount rejects with the SAME
// sentinel. If someone reimplements parsing inside ParseAmount instead of
// delegating, the two guards drift apart and this test catches it.
func TestParseAmount_InheritsParseContract(t *testing.T) {
	inputs := []string{
		"not-a-number",
		"",
		"1e100000000",
		"1234567890123456789012345678901234567890",
		"12.34.56",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			_, errParse := decimalx.Parse(input)
			if errParse == nil {
				t.Fatalf("Parse(%q) did not fail: the case is no longer useful as anti-drift", input)
			}
			_, errAmount := decimalx.ParseAmount(input, 2)
			if !errors.Is(errAmount, errParse) && !errors.Is(errAmount, decimalx.ErrNotParseable) && !errors.Is(errAmount, decimalx.ErrOutOfRange) {
				t.Fatalf("ParseAmount(%q, 2) err = %v, want the same sentinel as Parse: %v", input, errAmount, errParse)
			}
			// The sentinel must match by class: if Parse says out of range,
			// ParseAmount must too; if it says not parseable, likewise.
			if errors.Is(errParse, decimalx.ErrOutOfRange) && !errors.Is(errAmount, decimalx.ErrOutOfRange) {
				t.Fatalf("ParseAmount(%q, 2) lost Parse's anti-DoS bound: err = %v", input, errAmount)
			}
			if errors.Is(errParse, decimalx.ErrNotParseable) && !errors.Is(errAmount, decimalx.ErrNotParseable) {
				t.Fatalf("ParseAmount(%q, 2) lost Parse's ErrNotParseable: err = %v", input, errAmount)
			}
		})
	}
}

// TestParseAmount_DoesNotChangeParseContract: ParseAmount ADDS the money
// rule; it does not leak it back into Parse. Parse remains the anti-DoS
// guard for callers who do NOT persist money (a query filter, a third-party
// feed with its own contract), and those keep accepting the precision they
// always accepted.
func TestParseAmount_DoesNotChangeParseContract(t *testing.T) {
	for _, input := range []string{"33.334", "0.00000001", "0.004"} {
		d, err := decimalx.Parse(input)
		if err != nil {
			t.Fatalf("Parse(%q) err = %v, want nil: Parse does not carry the money rule", input, err)
		}
		if want := decimal.RequireFromString(input); !d.Equal(want) {
			t.Fatalf("Parse(%q) = %s, want %s", input, d, want)
		}
	}
}

// TestValidateAmount_MirrorsMoneyRuleOnAConstructedDecimal fixes the
// contract of ValidateAmount: it is ParseAmount's sibling for a decimal that
// did NOT arrive through a string.
//
// In a bulk load the `amount` field is typed decimal.Decimal, so
// encoding/json builds it directly and ParseAmount never sees it. That value
// ends up in the same scale-2 monetary column, so it deserves THE SAME rule.
// The criterion is d != round(d, scale), NOT exp < -scale: "100.500" has
// exponent -3 but is worth 100.50 exactly, so it is ACCEPTED. This table
// deliberately mirrors ParseAmount's: the two doors must let through exactly
// the same values.
func TestValidateAmount_MirrorsMoneyRuleOnAConstructedDecimal(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr error // nil => accepted
	}{
		{"integer, no decimals", "100", nil},
		{"one decimal", "100.5", nil},
		{"exactly two decimals", "100.50", nil},
		{"three decimals but exact at scale 2 (padding zeros)", "100.500", nil},
		{"zero", "0", nil},
		{"negative with two decimals", "-100.50", nil},

		{"three significant decimals", "100.005", decimalx.ErrTooManyDecimals},
		{"third decimal the DB would round away", "33.334", decimalx.ErrTooManyDecimals},
		{"negative with a third decimal", "-33.334", decimalx.ErrTooManyDecimals},
		{"third decimal on a near-zero value", "0.004", decimalx.ErrTooManyDecimals},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := decimalx.ValidateAmount(decimal.RequireFromString(c.input), 2)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("ValidateAmount(%s, 2) err = %v, want %v", c.input, err, c.wantErr)
			}
		})
	}
}

// TestValidateAmount_AppliesBoundBeforeRounding is the anti-DoS and
// anti-drift test at once.
//
// ValidateAmount receives an already-built decimal, which is exactly the
// kind that arrives without a bound: shopspring stores (coefficient,
// exponent) lazily, so `{"amount": 1e100000000}` deserializes for free and
// hangs later, when something materializes the digits. Round(scale) is one
// of those things.
//
// That is why ValidateBounds — O(1), one read of the exponent — must run
// BEFORE the Round. If the order is inverted, this test hangs instead of
// failing: the timeout still catches it.
//
// The expected error is ErrOutOfRange, not ErrTooManyDecimals: the BOUND
// rejects it, not the money rule. And the test NEVER materializes the value:
// it never compares it, prints it, or serializes it.
func TestValidateAmount_AppliesBoundBeforeRounding(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"huge exponent (the one that hangs on materialization)", "1e100000000"},
		{"huge negative exponent", "1e-100000000"},
		{"exponent barely out of bounds", "1e9"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- decimalx.ValidateAmount(decimal.RequireFromString(c.input), 2) }()

			select {
			case err := <-done:
				if !errors.Is(err, decimalx.ErrOutOfRange) {
					t.Fatalf("ValidateAmount(%s, 2) err = %v, want ErrOutOfRange (the bound, not the money rule)", c.input, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("ValidateAmount(%s, 2) hung (>5s): the bound ran AFTER Round and materialized the digits", c.input)
			}
		})
	}
}

// TestValidateAmount_DoesNotDuplicateTheBound: ValidateAmount delegates the
// bound to ValidateBounds. Everything ValidateBounds rejects, ValidateAmount
// rejects with the SAME sentinel. If someone copies the constants instead of
// delegating, the two guards drift and this test catches it.
func TestValidateAmount_DoesNotDuplicateTheBound(t *testing.T) {
	for _, input := range []string{"1e9", "1e-9", "1e100000000"} {
		t.Run(input, func(t *testing.T) {
			d := decimal.RequireFromString(input)
			errBounds := decimalx.ValidateBounds(d)
			if errBounds == nil {
				t.Fatalf("ValidateBounds(%s) did not fail: the case is no longer useful as anti-drift", input)
			}
			if errAmount := decimalx.ValidateAmount(d, 2); !errors.Is(errAmount, errBounds) {
				t.Fatalf("ValidateAmount(%s, 2) err = %v, want the same sentinel as ValidateBounds: %v",
					input, errAmount, errBounds)
			}
		})
	}
}

// TestValidateAmount_AndParseAmount_AgreeOnTheSameValue closes the circle
// between the money rule's two doors: the same number must run the same
// fate whether it enters through a string (ParseAmount) or already
// constructed by encoding/json (ValidateAmount). If they diverge, the same
// amount is accepted by the bulk path and rejected by the single POST — or
// vice versa — and the contract stops being one.
func TestValidateAmount_AndParseAmount_AgreeOnTheSameValue(t *testing.T) {
	for _, input := range []string{"100", "100.5", "100.50", "100.500", "0", "-100.50", "100.005", "33.334", "0.004"} {
		t.Run(input, func(t *testing.T) {
			_, errParse := decimalx.ParseAmount(input, 2)
			errValidate := decimalx.ValidateAmount(decimal.RequireFromString(input), 2)

			if (errParse == nil) != (errValidate == nil) {
				t.Fatalf("%q: ParseAmount err = %v but ValidateAmount err = %v — the two doors diverge",
					input, errParse, errValidate)
			}
			if errParse != nil && !errors.Is(errParse, decimalx.ErrTooManyDecimals) {
				t.Fatalf("%q: ParseAmount err = %v, want ErrTooManyDecimals", input, errParse)
			}
			if errValidate != nil && !errors.Is(errValidate, decimalx.ErrTooManyDecimals) {
				t.Fatalf("%q: ValidateAmount err = %v, want ErrTooManyDecimals", input, errValidate)
			}
		})
	}
}

// TestValidateAmount_NegativeScale documents that a negative scale is
// accepted and delegated to decimal.Decimal.Round, which rounds to the left
// of the decimal point (round(1234, -2) == 1200). decimalx does not reject
// it: the anti-DoS bound already ran first, so Round operates on a
// coefficient of at most 32 digits regardless of the scale's sign.
func TestValidateAmount_NegativeScale(t *testing.T) {
	if err := decimalx.ValidateAmount(decimal.RequireFromString("1200"), -2); err != nil {
		t.Errorf("ValidateAmount(1200, -2) should be accepted, err = %v", err)
	}
	if err := decimalx.ValidateAmount(decimal.RequireFromString("1234"), -2); !errors.Is(err, decimalx.ErrTooManyDecimals) {
		t.Errorf("ValidateAmount(1234, -2) err = %v, want ErrTooManyDecimals", err)
	}
}

// TestPaddingBeyondTheExponentBoundIsRejectedByBoundsFirst pins the exact
// edge where "pad with zeros" stops being harmless. The exponent bound
// applies to the LITERAL's exponent, not to whether the value happens to be
// exact at the target scale, and ValidateBounds runs BEFORE the money rule
// (see ValidateAmount/ParseAmount's doc). "100.500" has exponent -3, well
// within the default [-8, 8] range, and is exact at scale 2, so it is
// accepted. "100.000000000" (9 fractional zeros) is ALSO exact at scale 2 —
// 100 dollars, nothing lost by rounding — but its literal's exponent is -9,
// one past the default minExp of -8, so it is rejected with ErrOutOfRange by
// the bounds check, and the money rule never gets a chance to accept it.
// This is ported as-is from go-crucible and is intentional: the bounds check
// does not know or care that the value would survive rounding to scale.
func TestPaddingBeyondTheExponentBoundIsRejectedByBoundsFirst(t *testing.T) {
	if _, err := decimalx.ParseAmount("100.500", 2); err != nil {
		t.Fatalf("ParseAmount(100.500, 2) err = %v, want nil: exponent -3 is within the default bounds", err)
	}
	if _, err := decimalx.ParseAmount("100.000000000", 2); !errors.Is(err, decimalx.ErrOutOfRange) {
		t.Fatalf("ParseAmount(100.000000000, 2) err = %v, want ErrOutOfRange: exponent -9 exceeds the default minExp of -8", err)
	}

	if err := decimalx.ValidateAmount(decimal.RequireFromString("100.500"), 2); err != nil {
		t.Fatalf("ValidateAmount(100.500, 2) err = %v, want nil", err)
	}
	if err := decimalx.ValidateAmount(decimal.RequireFromString("100.000000000"), 2); !errors.Is(err, decimalx.ErrOutOfRange) {
		t.Fatalf("ValidateAmount(100.000000000, 2) err = %v, want ErrOutOfRange", err)
	}
}
