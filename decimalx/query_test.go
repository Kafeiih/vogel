package decimalx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kafeiih/vogel/decimalx"
	"github.com/kafeiih/vogel/request"
)

// TestQuery_Absent verifies an absent query parameter returns nil with no
// error recorded, matching the other *Query helpers in request.Validator.
func TestQuery_Absent(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	v := request.NewValidator()

	got := decimalx.Query(v, r, "amount")

	if got != nil {
		t.Errorf("Query() = %v, want nil for an absent parameter", got)
	}
	if v.HasErrors() {
		t.Errorf("Query() recorded an error for an absent parameter: %v", v.Errors())
	}
}

// TestQuery_Empty verifies an empty query parameter behaves like an absent
// one: nil, no error.
func TestQuery_Empty(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?amount=", nil)
	v := request.NewValidator()

	got := decimalx.Query(v, r, "amount")

	if got != nil {
		t.Errorf("Query() = %v, want nil for an empty parameter", got)
	}
	if v.HasErrors() {
		t.Errorf("Query() recorded an error for an empty parameter: %v", v.Errors())
	}
}

// TestQuery_Valid verifies a valid decimal string is parsed and returned.
func TestQuery_Valid(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?amount=100.50", nil)
	v := request.NewValidator()

	got := decimalx.Query(v, r, "amount")

	if got == nil {
		t.Fatal("Query() = nil, want a parsed decimal")
	}
	if got.String() != "100.5" {
		t.Errorf("Query() = %s, want 100.5", got.String())
	}
	if v.HasErrors() {
		t.Errorf("Query() recorded an error for a valid value: %v", v.Errors())
	}
}

// TestQuery_Negative verifies negative values are allowed: monto_total is
// legitimately negative for a credit note, and Query must not reject it.
func TestQuery_Negative(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?amount=-42.50", nil)
	v := request.NewValidator()

	got := decimalx.Query(v, r, "amount")

	if got == nil {
		t.Fatal("Query() = nil, want a parsed negative decimal")
	}
	if got.String() != "-42.5" {
		t.Errorf("Query() = %s, want -42.5", got.String())
	}
	if v.HasErrors() {
		t.Errorf("Query() recorded an error for a valid negative value: %v", v.Errors())
	}
}

// TestQuery_Invalid verifies a syntactically invalid value returns nil and
// records the InvalidDecimal message on the given field.
func TestQuery_Invalid(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?amount=not-a-number", nil)
	v := request.NewValidator()

	got := decimalx.Query(v, r, "amount")

	if got != nil {
		t.Errorf("Query() = %v, want nil for an invalid value", got)
	}
	if !v.HasErrors() {
		t.Fatal("Query() did not record an error for an invalid value")
	}
	if want := "amount must be a valid decimal"; v.Errors()["amount"] != want {
		t.Errorf("Query() error = %q, want %q", v.Errors()["amount"], want)
	}
}

// TestQuery_OutOfRange verifies a syntactically valid but out-of-bounds
// value (the anti-DoS case) also returns nil and records InvalidDecimal,
// not a distinct message — the caller only needs to know the field failed.
func TestQuery_OutOfRange(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?amount=1e100000000", nil)
	v := request.NewValidator()

	got := decimalx.Query(v, r, "amount")

	if got != nil {
		t.Errorf("Query() = %v, want nil for an out-of-range value", got)
	}
	if want := "amount must be a valid decimal"; v.Errors()["amount"] != want {
		t.Errorf("Query() error = %q, want %q", v.Errors()["amount"], want)
	}
}

// TestQuery_CustomInvalidDecimalMessage verifies Query records the error
// using the Validator's own configured Messages, via
// request.NewDecoder(...).NewValidator(), rather than hardcoding English
// text — this is the whole reason request.Messages().InvalidDecimal exists.
func TestQuery_CustomInvalidDecimalMessage(t *testing.T) {
	d := request.New(request.WithMessages(request.Messages{
		InvalidDecimal: func(field string) string { return "custom decimal: " + field },
	}))
	r := httptest.NewRequest(http.MethodGet, "/?amount=nope", nil)
	v := d.NewValidator()

	got := decimalx.Query(v, r, "amount")

	if got != nil {
		t.Errorf("Query() = %v, want nil for an invalid value", got)
	}
	if want := "custom decimal: amount"; v.Errors()["amount"] != want {
		t.Errorf("Query() error = %q, want %q", v.Errors()["amount"], want)
	}
}
