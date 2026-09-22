package decimalx_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/kafeiih/vogel/decimalx"
)

// TestValidateAmount_ScaleCeiling pins the guard that stops a
// caller-supplied scale from being handed to decimal.Decimal.Round
// unbounded. Round(scale) rescales the coefficient by roughly
// 10^(exponent-scale) digits, so a scale far from the value's own exponent
// materializes that many digits even though ValidateBounds already accepted
// d itself: scale is a second, independent axis of the same attack.
// ±MaxExponentLimit is accepted (the same ceiling NewBounds enforces on
// minExp/maxExp), one past it on either side is rejected with
// ErrInvalidScale, and the pathological extremes (math.MinInt32,
// math.MaxInt32) are rejected too.
func TestValidateAmount_ScaleCeiling(t *testing.T) {
	// Zero is worth the same at any scale, positive or negative
	// (d.Round(scale) never changes it), so it isolates the scale guard
	// itself from the unrelated money-rule criterion (d != d.Round(scale)):
	// a value like "100.50" would legitimately fail that criterion at a
	// scale of -1000 (rounding it to the left of the decimal point changes
	// it), which is not what this test is pinning.
	d := decimal.Zero

	for _, scale := range []int32{-decimalx.MaxExponentLimit, decimalx.MaxExponentLimit} {
		if err := decimalx.ValidateAmount(d, scale); err != nil {
			t.Errorf("ValidateAmount(0, %d) err = %v, want nil: exactly at the scale ceiling must be accepted", scale, err)
		}
	}

	for _, scale := range []int32{-(decimalx.MaxExponentLimit + 1), decimalx.MaxExponentLimit + 1, math.MinInt32, math.MaxInt32} {
		if err := decimalx.ValidateAmount(d, scale); !errors.Is(err, decimalx.ErrInvalidScale) {
			t.Errorf("ValidateAmount(0, %d) err = %v, want ErrInvalidScale", scale, err)
		}
	}
}

// TestParseAmount_ScaleCeiling mirrors TestValidateAmount_ScaleCeiling over
// the string door: ParseAmount delegates to ValidateAmount, so it must
// reject the same out-of-range scales with the same sentinel.
func TestParseAmount_ScaleCeiling(t *testing.T) {
	// See TestValidateAmount_ScaleCeiling for why "0" (not "100.50") is the
	// right fixture here: it is worth the same at any scale, so it isolates
	// the scale guard from the unrelated money-rule criterion.
	for _, scale := range []int32{-decimalx.MaxExponentLimit, decimalx.MaxExponentLimit} {
		if _, err := decimalx.ParseAmount("0", scale); err != nil {
			t.Errorf("ParseAmount(0, %d) err = %v, want nil: exactly at the scale ceiling must be accepted", scale, err)
		}
	}

	for _, scale := range []int32{-(decimalx.MaxExponentLimit + 1), decimalx.MaxExponentLimit + 1, math.MinInt32, math.MaxInt32} {
		if _, err := decimalx.ParseAmount("0", scale); !errors.Is(err, decimalx.ErrInvalidScale) {
			t.Errorf("ParseAmount(0, %d) err = %v, want ErrInvalidScale", scale, err)
		}
	}
}

// TestValidateAmount_ScaleCeiling_RejectsFast is the timing guard for the
// scale check itself: it must run BEFORE Round, so an out-of-range scale on
// an otherwise-hostile-to-round combination returns immediately rather than
// materializing anything. The goroutine only sends its result over a
// channel; every assertion happens in the test goroutine after receiving,
// so a timeout never causes a log-after-test-completed panic.
func TestValidateAmount_ScaleCeiling_RejectsFast(t *testing.T) {
	d := decimal.RequireFromString("100.50")

	done := make(chan error, 1)
	go func() {
		done <- decimalx.ValidateAmount(d, math.MaxInt32)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, decimalx.ErrInvalidScale) {
			t.Errorf("ValidateAmount(100.50, MaxInt32) err = %v, want ErrInvalidScale", err)
		}
	case <-time.After(timingBudget):
		t.Fatal("ValidateAmount(100.50, MaxInt32) did not return within the timing budget: the scale guard ran too late")
	}
}

// TestParseAmount_ScaleCeiling_RejectsFast mirrors the timing guard over the
// string door.
func TestParseAmount_ScaleCeiling_RejectsFast(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := decimalx.ParseAmount("100.50", math.MinInt32)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, decimalx.ErrInvalidScale) {
			t.Errorf("ParseAmount(100.50, MinInt32) err = %v, want ErrInvalidScale", err)
		}
	case <-time.After(timingBudget):
		t.Fatal("ParseAmount(100.50, MinInt32) did not return within the timing budget: the scale guard ran too late")
	}
}
