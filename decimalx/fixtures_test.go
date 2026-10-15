package decimalx_test

import (
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// decimalOf builds the decimal a test case names. A "1e<N>" literal whose
// exponent is past decimal.MaxDecodeExponent is built with decimal.New,
// because since shopspring/decimal v1.5.0 NewFromString (and so
// RequireFromString) refuses it; these are exactly the hanging values the
// validators must reject, so the tests still need to construct them. Any
// other literal goes through decimal.RequireFromString.
func decimalOf(s string) decimal.Decimal {
	if rest, ok := strings.CutPrefix(s, "1e"); ok {
		if exp, err := strconv.ParseInt(rest, 10, 32); err == nil &&
			(exp > int64(decimal.MaxDecodeExponent) || exp < -int64(decimal.MaxDecodeExponent)) {
			return decimal.New(1, int32(exp))
		}
	}
	return decimal.RequireFromString(s)
}
