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
// # Bounds cannot be switched off
//
// The default bounds (DefaultBounds) allow a 32-character coefficient and an
// exponent in [-8, 8] — generous for any legitimate amount, and both O(1) to
// check. A caller may override them through NewBounds, but NewBounds rejects
// an unbounded or inverted configuration (a zero or negative length, or
// minExp > maxExp): a library must never let a caller disable its own
// anti-DoS guard. Bounds' fields are unexported, and every method treats a
// zero-value Bounds (however a caller ends up with one — a bare var, or a
// discarded NewBounds error) as DefaultBounds, so there is no way to reach
// an unbounded parser through this package's exported API.
package decimalx
