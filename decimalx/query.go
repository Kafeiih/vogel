package decimalx

import (
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/kafeiih/vogel/request"
)

// Query extracts and validates a decimal query parameter using
// DefaultBounds. It returns nil if the parameter is absent or empty, with
// no error recorded — the caller decides what an absent value means.
//
// A value present but not a syntactically valid, in-bounds decimal (see
// Parse) records an InvalidDecimal error on v, using v's own configured
// Messages (Validator.Messages), and returns nil.
//
// Query does NOT reject negative values: an amount such as a credit note's
// total is legitimately negative.
//
// Query lives here, as a function taking *request.Validator, rather than as
// a method on Validator itself, so that request does not need to import
// shopspring/decimal.
func Query(v *request.Validator, r *http.Request, param string) *decimal.Decimal {
	raw := r.URL.Query().Get(param)
	if raw == "" {
		return nil
	}
	d, err := Parse(raw)
	if err != nil {
		v.AddError(param, v.Messages().InvalidDecimal(param))
		return nil
	}
	return &d
}
