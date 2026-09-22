package decimalx

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

var (
	// ErrOutOfRange is returned when a value parses but falls outside the
	// configured bounds: a coefficient too long, or an exponent outside
	// [MinExp, MaxExp]. These are exactly the values that hang a worker when
	// materialized.
	ErrOutOfRange = errors.New("decimal out of range: coefficient or exponent exceeds the configured bounds")

	// ErrNotParseable is returned when the string is not a syntactically
	// valid decimal. It wraps the underlying decimal.NewFromString error, so
	// errors.Is(err, ErrNotParseable) succeeds while the original error
	// remains inspectable via errors.Unwrap.
	ErrNotParseable = errors.New("decimal not parseable")

	// ErrTooManyDecimals is returned by ValidateAmount/ParseAmount when the
	// value carries more precision than the target scale can hold without
	// rounding it. Each caller can wrap it in its own application-level
	// sentinel to map it to the HTTP status that fits.
	ErrTooManyDecimals = errors.New("amount has more decimals than the configured scale allows")

	// ErrInvalidBounds is returned by NewBounds when the requested
	// configuration would disable the anti-DoS guard: a maxLen that is zero
	// or negative, or an exponent range where minExp > maxExp.
	ErrInvalidBounds = errors.New("invalid decimal bounds")
)

// Default bounds. A 32-character coefficient and an exponent in [-8, 8] are
// generous for any legitimate amount, and both are O(1) to check. See the
// package doc for why they must never be relaxed to "unbounded": they are
// the anti-DoS barrier.
const (
	defaultMaxLen = 32
	defaultMinExp = -8
	defaultMaxExp = 8
)

// MaxExponentLimit is the hard ceiling NewBounds enforces on minExp and
// maxExp: NewBounds rejects any configuration with minExp < -MaxExponentLimit
// or maxExp > MaxExponentLimit with ErrInvalidBounds, regardless of what a
// caller requests. 10^1000 still materializes in microseconds, so any Bounds
// reachable through this package's exported API stays cheap to round and
// print — the ceiling is what keeps NewBounds from being usable to reopen the
// exact DoS this package exists to close (see the package doc).
const MaxExponentLimit int32 = 1000

// Bounds configures the limits Parse, ValidateBounds, ValidateAmount and
// ParseAmount enforce: the maximum coefficient length (MaxLen, as returned
// by decimal.Decimal.NumDigits) and the exponent range [MinExp, MaxExp].
//
// Bounds' fields are unexported so the only way to obtain one from outside
// this package is DefaultBounds or NewBounds — and every method on Bounds
// treats a zero-value Bounds (a bare "var b decimalx.Bounds", or the value
// NewBounds returns alongside a non-nil error) as DefaultBounds. That makes
// it impossible to end up with an unbounded parser through this package's
// exported API: either NewBounds validated the configuration, or the
// zero-value fallback applies the safe defaults.
type Bounds struct {
	maxLen int
	minExp int32
	maxExp int32
}

// DefaultBounds returns the bounds every package-level function in this
// package uses: a 32-character coefficient and an exponent in [-8, 8].
func DefaultBounds() Bounds {
	return Bounds{maxLen: defaultMaxLen, minExp: defaultMinExp, maxExp: defaultMaxExp}
}

// NewBounds builds a custom Bounds. It rejects any configuration that would
// disable the anti-DoS guard: maxLen must be positive, minExp must not exceed
// maxExp, and neither minExp nor maxExp may cross MaxExponentLimit (a hard
// ceiling of ±1000 — see MaxExponentLimit's doc for why). On error it returns
// the zero-value Bounds, which — per this package's safety guarantee — still
// behaves like DefaultBounds if a caller discards the error and uses it
// anyway.
func NewBounds(maxLen int, minExp, maxExp int32) (Bounds, error) {
	if maxLen <= 0 {
		return Bounds{}, fmt.Errorf("%w: maxLen must be positive, got %d", ErrInvalidBounds, maxLen)
	}
	if minExp > maxExp {
		return Bounds{}, fmt.Errorf("%w: minExp (%d) must not exceed maxExp (%d)", ErrInvalidBounds, minExp, maxExp)
	}
	if minExp < -MaxExponentLimit || maxExp > MaxExponentLimit {
		return Bounds{}, fmt.Errorf("%w: exponent range [%d, %d] exceeds the hard ceiling of ±%d",
			ErrInvalidBounds, minExp, maxExp, MaxExponentLimit)
	}
	return Bounds{maxLen: maxLen, minExp: minExp, maxExp: maxExp}, nil
}

// effective returns b, or DefaultBounds if b is the zero value. maxLen == 0
// only ever happens on a zero-value Bounds, since NewBounds refuses to build
// one with a non-positive maxLen: it is therefore a safe, unambiguous
// signal that b was never constructed through this package's API.
func (b Bounds) effective() Bounds {
	if b.maxLen == 0 {
		return DefaultBounds()
	}
	return b
}

// Parse converts a string to a decimal.Decimal, rejecting values that,
// although cheap to parse, hang a worker once materialized (comparison,
// String, a database NUMERIC codec).
//
// It applies three guards, in this order:
//  1. len(s) > MaxLen -> ErrOutOfRange, BEFORE constructing anything. This
//     bounds the coefficient's digit count ahead of construction, which is
//     what keeps construction itself cheap.
//  2. decimal.NewFromString(s) erroring -> ErrNotParseable.
//  3. ValidateBounds(d) -> ErrOutOfRange. The bounds live there and are not
//     repeated here: they are the same bounds an already-constructed
//     decimal must satisfy (see ValidateBounds).
//
// Guard 1 means ValidateBounds' coefficient bound can never fire from this
// path — a string within MaxLen characters cannot yield more than MaxLen
// digits — so delegating does not change Parse's behavior; it only avoids
// defining the bound twice.
func (b Bounds) Parse(s string) (decimal.Decimal, error) {
	b = b.effective()
	if len(s) > b.maxLen {
		return decimal.Zero, ErrOutOfRange
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, fmt.Errorf("%w: %w", ErrNotParseable, err)
	}
	if err := b.ValidateBounds(d); err != nil {
		return decimal.Zero, err
	}
	return d, nil
}

// ValidateBounds applies b's bounds to an already-constructed decimal. It is
// the single definition of the bounds; Parse calls it instead of duplicating
// them.
//
// It exists because Parse only guards the string door, and not every
// decimal enters through one: in a bulk load, a field typed decimal.Decimal
// is built directly by encoding/json, without going through this package.
// That decimal arrives UNBOUNDED, and a payload like
// {"amount": 1e100000000} hangs a worker the moment something materializes
// it (a database driver encoding it for a NUMERIC column, for example).
// Every decimal of external origin that did not pass through Parse MUST
// pass through here.
//
// It applies the two guards in this order, which matters:
//  1. Exponent() outside [MinExp, MaxExp] -> ErrOutOfRange. This is the
//     anti-DoS guard and goes FIRST: a field read, O(1), that materializes
//     nothing. It is what stops the ~20-byte payload with unbounded work.
//  2. NumDigits() > MaxLen -> ErrOutOfRange. Mirrors Parse's length bound.
//     NumDigits only looks at the COEFFICIENT (it never computes 10^exp),
//     and that coefficient is already materialized by the deserializer and
//     bounded by the request body's size limit, so its cost is proportional
//     to bytes the client already paid for: it does not amplify.
func (b Bounds) ValidateBounds(d decimal.Decimal) error {
	b = b.effective()
	if exp := d.Exponent(); exp > b.maxExp || exp < b.minExp {
		return ErrOutOfRange
	}
	if d.NumDigits() > b.maxLen {
		return ErrOutOfRange
	}
	return nil
}

// ValidateAmount applies b's bounds to d, then rejects it unless it fits
// exactly in scale decimals: the money rule for a decimal that did NOT
// arrive through a string (see ParseAmount for the string door, and the
// package doc for why the two doors share one definition).
//
// ValidateBounds runs FIRST because Round MATERIALIZES the digits: on an
// unbounded value, "1e100000000" hangs the worker when rounded — the same
// DoS Parse/ValidateBounds exist to stop. The cheap bound must run before
// the expensive operation.
//
// The rejection criterion is d != d.Round(scale), NOT "the literal's
// exponent is more precise than scale". "100.500" at scale 2 has exponent
// -3 but is worth exactly 100.50, and is ACCEPTED: rejecting it would
// punish a caller who pads with zeros. Only a value that actually CHANGES
// when rounded to scale is rejected, because that is the only case where
// value would silently evaporate.
//
// scale may be negative, in which case it is passed through to
// decimal.Decimal.Round as-is (rounding to the left of the decimal point).
// That is not a DoS concern: ValidateBounds already ran, so Round always
// operates on a coefficient of at most MaxLen digits, regardless of scale's
// sign.
//
// Because ValidateBounds runs FIRST, padding a literal with zeros beyond the
// configured exponent bound is rejected by the bounds check, not by this
// rule, even when the padded value is exact at scale: "100.500" (exponent
// -3) is within the default [-8, 8] range and accepted, but
// "100.000000000" (exponent -9) is rejected with ErrOutOfRange before this
// method's own criterion ever runs, despite being equally exact at scale 2.
func (b Bounds) ValidateAmount(d decimal.Decimal, scale int32) error {
	b = b.effective()
	if err := b.ValidateBounds(d); err != nil {
		return err
	}
	if !d.Equal(d.Round(scale)) {
		return ErrTooManyDecimals
	}
	return nil
}

// ParseAmount is Parse plus the money rule: the value must fit in the target
// scale without losing precision. It is the door every money-carrying
// string must pass through; Parse alone is for values that are not
// persisted as money.
//
// The bounds check inside ValidateAmount re-runs a check Parse already
// performed; that is intentional (see the package doc) and cheap: it is
// O(1) and cannot change the result, since Parse already guaranteed d
// satisfies b's bounds.
//
// As with ValidateAmount, a literal padded with zeros beyond the configured
// exponent bound is rejected by that same bounds check, before the money
// rule gets a chance to accept it: "100.000000000" is exact at scale 2 but
// its exponent (-9) exceeds the default minExp of -8, so it is rejected with
// ErrOutOfRange.
func (b Bounds) ParseAmount(s string, scale int32) (decimal.Decimal, error) {
	b = b.effective()
	d, err := b.Parse(s)
	if err != nil {
		return decimal.Zero, err
	}
	if err := b.ValidateAmount(d, scale); err != nil {
		return decimal.Zero, err
	}
	return d, nil
}

// Parse delegates to Bounds.Parse using DefaultBounds.
func Parse(s string) (decimal.Decimal, error) {
	return DefaultBounds().Parse(s)
}

// ValidateBounds delegates to Bounds.ValidateBounds using DefaultBounds.
func ValidateBounds(d decimal.Decimal) error {
	return DefaultBounds().ValidateBounds(d)
}

// ValidateAmount delegates to Bounds.ValidateAmount using DefaultBounds.
func ValidateAmount(d decimal.Decimal, scale int32) error {
	return DefaultBounds().ValidateAmount(d, scale)
}

// ParseAmount delegates to Bounds.ParseAmount using DefaultBounds.
func ParseAmount(s string, scale int32) (decimal.Decimal, error) {
	return DefaultBounds().ParseAmount(s, scale)
}
