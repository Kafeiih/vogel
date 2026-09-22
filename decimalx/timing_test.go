package decimalx_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/kafeiih/vogel/decimalx"
)

// timingBudget is generous relative to the O(1) guards under test (a few
// microseconds in practice): it exists to catch a real hang (the digits
// being materialized), not to assert a tight performance number that would
// make this test flaky under load. 2 seconds is enough to catch a genuine
// hang while staying tolerant of -race instrumentation and a loaded CI
// runner.
const timingBudget = 2 * time.Second

// TestParse_HangingValueReturnsQuickly is the timing guard for the string
// door: Parse("1e100000000") must reject in well under a second, because the
// length guard runs before decimal.NewFromString ever touches the string,
// and the exponent guard is an O(1) field read.
func TestParse_HangingValueReturnsQuickly(t *testing.T) {
	done := make(chan struct{})
	go func() {
		_, _ = decimalx.Parse("1e100000000")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timingBudget):
		t.Fatal("Parse(\"1e100000000\") did not return within the timing budget: it is materializing digits")
	}
}

// TestValidateBounds_HangingDecimalReturnsQuickly is the timing guard for a
// decimal built directly with decimal.New (as encoding/json would build a
// decimal.Decimal field), never through Parse's string door.
// decimal.New(1, 100000000) means 1 * 10^100000000 — an exponent large
// enough that Round or String would hang materializing it.
func TestValidateBounds_HangingDecimalReturnsQuickly(t *testing.T) {
	hostile := decimal.New(1, 100000000)

	done := make(chan struct{})
	go func() {
		_ = decimalx.ValidateBounds(hostile)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timingBudget):
		t.Fatal("ValidateBounds(decimal.New(1, 100000000)) did not return within the timing budget")
	}
}

// TestValidateAmount_HangingDecimalReturnsQuickly verifies ValidateAmount
// rejects the same hostile, directly-constructed decimal before Round(scale)
// ever runs.
func TestValidateAmount_HangingDecimalReturnsQuickly(t *testing.T) {
	hostile := decimal.New(1, 100000000)

	done := make(chan struct{})
	go func() {
		_ = decimalx.ValidateAmount(hostile, 2)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timingBudget):
		t.Fatal("ValidateAmount(decimal.New(1, 100000000), 2) did not return within the timing budget")
	}
}

// TestParseAmount_HangingValueReturnsQuickly verifies ParseAmount rejects a
// hostile string before either Parse's construction or Round(scale) runs.
func TestParseAmount_HangingValueReturnsQuickly(t *testing.T) {
	done := make(chan struct{})
	go func() {
		_, _ = decimalx.ParseAmount("1e100000000", 2)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timingBudget):
		t.Fatal("ParseAmount(\"1e100000000\", 2) did not return within the timing budget")
	}
}
