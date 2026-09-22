// Package decimalx provides a bounded parser for money-shaped strings
// coming from an HTTP request, plus the money-precision rule that goes with
// it.
//
// # The attack
//
// shopspring/decimal stores a decimal.Decimal lazily, as a (coefficient,
// exponent) pair: parsing "1e100000000" returns in microseconds without
// materializing a single digit. But ANY operation that materializes those
// digits hangs a worker on a ~20-byte payload:
//   - Equal/Cmp (both operands must be rescaled to a common exponent)
//   - StringFixed/String (serializing the response)
//   - a database driver's NUMERIC codec, encoding the value for the wire
//
// That is why every decimal string coming from an HTTP request MUST go
// through Parse before it is compared, serialized, or persisted.
//
// Not every decimal arrives through a string, though: when a request body
// field is typed decimal.Decimal, encoding/json builds it directly and Parse
// never sees it. That decimal arrives UNBOUNDED. ValidateBounds exists for
// that path: it applies the SAME bounds to an already-constructed value. The
// bounds are defined once, in ValidateBounds, and Parse delegates to it —
// there is no third option for a decimal of external origin.
//
// # The money rule
//
// On top of that bound sits the MONEY RULE: a monetary column typically
// rounds silently on write, at a fixed scale, while validation must run in
// memory at the parsed precision. Whatever does not fit in that scale is
// REJECTED, not rounded — rounding silently loses the client money without
// telling them.
//
// The money rule has two doors, one per origin, both sharing one definition
// of the criterion:
//   - ParseAmount(s, scale) — for money entering as a string. It is Parse
//     plus ValidateAmount.
//   - ValidateAmount(d, scale) — for money that arrives already constructed
//     (a bulk load). It is ValidateBounds plus the rejection of whatever does
//     not survive rounding to scale.
//
// Bounding is mandatory for everyone (Parse or ValidateBounds). The money
// rule is additionally mandatory for whoever WRITES money (ParseAmount or
// ValidateAmount). Parse and ValidateBounds alone are for values that are
// not persisted as money: a query filter, a third-party feed with its own
// contract.
//
// The two doors do NOT accept exactly the same set of strings/values,
// though: Parse's pre-check bounds the STRING's length (a sign or a decimal
// point counts as a character), while ValidateBounds' bound looks only at
// the coefficient's DIGITS (NumDigits never counts the sign). A 32-digit
// negative value is 33 characters — rejected by Parse/ParseAmount — but its
// decoded VALUE, arriving through ValidateBounds/ValidateAmount instead, has
// a 32-digit coefficient and is accepted. Parse's string door is therefore
// STRICTER than ValidateBounds' digit door, never looser (see Parse and
// ValidateBounds's own doc comments for the full statement).
//
// scale is itself bounds-checked, independently of d: ValidateAmount (and
// therefore ParseAmount) rejects any scale outside
// [-MaxExponentLimit, MaxExponentLimit] with ErrInvalidScale, before
// Round(scale) ever runs. Round rescales the coefficient by roughly
// 10^(exponent-scale) digits, so an unbounded scale would hang a worker on
// an otherwise-legitimate, already-bounded d — a second, independent axis of
// the same materialization attack.
//
// The bounds check runs BEFORE the money rule, which has a consequence worth
// stating plainly: padding a literal with zeros beyond the configured
// exponent bound is rejected by the bounds check, even when the padded value
// is exact at the target scale. "100.500" (exponent -3) is within the
// default [-8, 8] range and accepted; "100.000000000" (exponent -9) is
// rejected with ErrOutOfRange despite being equally exact at scale 2,
// because the exponent bound applies to the literal, not to the value's
// worth once rounded.
//
// # Bounds cannot be switched off
//
// The default bounds (DefaultBounds) allow a 32-character coefficient and an
// exponent in [-8, 8] — generous for any legitimate amount, and both O(1) to
// check. A caller may override them through NewBounds, but NewBounds rejects
// an unbounded or inverted configuration (a zero or negative length, minExp >
// maxExp, or either bound crossing MaxExponentLimit — a hard ceiling of
// ±1000, since even 10^1000 stays microseconds-cheap to round and print): a
// library must never let a caller disable its own anti-DoS guard. Bounds'
// fields are unexported, and every method treats a zero-value Bounds
// (however a caller ends up with one — a bare var, or a discarded NewBounds
// error) as DefaultBounds. Between the zero-value fallback and the
// MaxExponentLimit ceiling, there is no way to reach an unbounded parser
// through this package's exported API.
//
// scale carries its own, separate ceiling on ValidateAmount and ParseAmount,
// enforced the same way: any scale outside [-MaxExponentLimit,
// MaxExponentLimit] is rejected with ErrInvalidScale before it ever reaches
// decimal.Decimal.Round. Between the exponent ceiling on Bounds itself and
// the scale ceiling on the money rule, every Bounds value, constructed or
// not, and every scale this package accepts, keeps Parse, ValidateBounds,
// ValidateAmount and ParseAmount cheap.
package decimalx
