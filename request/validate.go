package request

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/kafeiih/vogel/httpx/response"
)

// FieldErrors maps field names to human-readable error messages.
type FieldErrors map[string]string

// Validator accumulates validation errors across multiple fields so a handler
// can validate every input before responding, instead of failing on the first
// bad field and forcing the client into a fix-one-error-per-request loop.
type Validator struct {
	errors   FieldErrors
	messages Messages
}

// NewValidator creates a ready-to-use Validator using DefaultMessages. Use
// Decoder.NewValidator (built via New and WithMessages) to localize the
// messages a Validator records.
func NewValidator() *Validator {
	return defaultDecoder.NewValidator()
}

// NewValidator creates a ready-to-use Validator that records errors using d's
// configured Messages.
func (d *Decoder) NewValidator() *Validator {
	return &Validator{errors: make(FieldErrors), messages: *d.msgs()}
}

// HasErrors reports whether any validation errors have been recorded.
func (v *Validator) HasErrors() bool {
	return len(v.errors) > 0
}

// Errors returns the accumulated field errors. It returns the live map, not
// a copy, so mutating it mutates the Validator's state. AddError is the
// supported way to add entries from outside this package — for example, a
// consuming application wrapping Validator with its own query-parameter
// parsers (an Int64Query, BoolQuery, or DecimalQuery that stays in that
// application rather than this package).
func (v *Validator) Errors() FieldErrors {
	return v.errors
}

// AddError records message as the error for field, overwriting any previous
// error recorded for that same field. It is the supported way for a consumer
// embedding Validator in its own parsers to report a validation failure
// through the same Validator instance, alongside this package's own checks.
func (v *Validator) AddError(field, message string) {
	v.errors[field] = message
}

// WriteErrors sends a 400 response with the accumulated field errors.
func (v *Validator) WriteErrors(w http.ResponseWriter, r *http.Request) {
	response.ValidationError(w, r, v.errors)
}

// UUIDParam extracts and validates a chi URL parameter as a UUID.
// Returns uuid.Nil if validation fails.
func (v *Validator) UUIDParam(r *http.Request, param string) uuid.UUID {
	raw := chi.URLParam(r, param)
	if raw == "" {
		v.AddError(param, v.messages.Required(param))
		return uuid.Nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		v.AddError(param, v.messages.InvalidUUID(param))
		return uuid.Nil
	}
	return id
}

// IntQuery extracts and validates an integer query parameter with a default value.
// Returns the default if the parameter is absent.
func (v *Validator) IntQuery(r *http.Request, param string, defaultVal int) int {
	raw := r.URL.Query().Get(param)
	if raw == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		v.AddError(param, v.messages.NotInteger(param))
		return defaultVal
	}
	if n < 0 {
		v.AddError(param, v.messages.Negative(param))
		return defaultVal
	}
	return n
}

// MaxInt clamps a value to the given maximum.
func (v *Validator) MaxInt(param string, val, max int) int {
	if val > max {
		return max
	}
	return val
}

// TimeQuery extracts and validates an RFC3339 time query parameter.
// Returns nil if the parameter is absent.
func (v *Validator) TimeQuery(r *http.Request, param string) *time.Time {
	raw := r.URL.Query().Get(param)
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		v.AddError(param, v.messages.InvalidRFC3339(param))
		return nil
	}
	return &t
}

// DateQuery extracts and validates a date query parameter in ISO format (2006-01-02).
// Returns nil if the parameter is absent.
func (v *Validator) DateQuery(r *http.Request, param string) *time.Time {
	raw := r.URL.Query().Get(param)
	if raw == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		v.AddError(param, v.messages.InvalidISODate(param))
		return nil
	}
	return &t
}

// UUIDQuery extracts and validates a query parameter as a UUID.
// Returns nil if the parameter is absent.
func (v *Validator) UUIDQuery(r *http.Request, param string) *uuid.UUID {
	raw := r.URL.Query().Get(param)
	if raw == "" {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		v.AddError(param, v.messages.InvalidUUID(param))
		return nil
	}
	return &id
}

// Int64Param extracts and validates a chi URL parameter as an int64.
// Returns 0 if validation fails.
func (v *Validator) Int64Param(r *http.Request, param string) int64 {
	raw := chi.URLParam(r, param)
	if raw == "" {
		v.AddError(param, v.messages.Required(param))
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		v.AddError(param, v.messages.InvalidInteger(param))
		return 0
	}
	return n
}

// Int64Query extracts and validates an int64 query parameter. Returns nil if
// the parameter is absent or empty, with no error recorded — the caller
// decides what an absent value means (unlike IntQuery, which takes a
// default). A value present but not a base-10 int64
// (strconv.ParseInt(raw, 10, 64)) records an InvalidInteger error and
// returns nil.
func (v *Validator) Int64Query(r *http.Request, param string) *int64 {
	raw := r.URL.Query().Get(param)
	if raw == "" {
		return nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		v.AddError(param, v.messages.InvalidInteger(param))
		return nil
	}
	return &n
}

// BoolQuery extracts and validates a boolean query parameter. Returns nil if
// the parameter is absent or empty, with no error recorded. It accepts
// exactly what strconv.ParseBool accepts (1, t, T, TRUE, true, True, 0, f, F,
// FALSE, false, False); anything else records an InvalidBoolean error and
// returns nil.
func (v *Validator) BoolQuery(r *http.Request, param string) *bool {
	raw := r.URL.Query().Get(param)
	if raw == "" {
		return nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		v.AddError(param, v.messages.InvalidBoolean(param))
		return nil
	}
	return &b
}

// Messages returns the effective Messages this Validator records field
// errors with: DefaultMessages merged with whatever WithMessages options
// built the Decoder that created it (see Decoder.NewValidator).
//
// It exists so a helper package that stays outside request — for example,
// decimalx, which must not be imported here because request must not depend
// on shopspring/decimal — can still record an error using this Validator's
// own configured wording instead of hardcoding English text:
//
//	v.AddError(param, v.Messages().InvalidDecimal(param))
func (v *Validator) Messages() Messages {
	return v.messages
}

// Enum validates that a string value is one of the allowed options.
// Returns the value as-is if empty (optional field) or if it matches.
func (v *Validator) Enum(param, value string, allowed []string) string {
	if value == "" {
		return value
	}
	for _, a := range allowed {
		if value == a {
			return value
		}
	}
	v.AddError(param, v.messages.NotAllowed(param))
	return value
}

// PublicIDParam extracts and validates a chi URL parameter as a UUID or ULID.
// Returns the raw string unchanged. Returns "" if validation fails.
func (v *Validator) PublicIDParam(r *http.Request, param string) string {
	raw := chi.URLParam(r, param)
	if raw == "" {
		v.AddError(param, v.messages.Required(param))
		return ""
	}
	if !isValidPublicID(raw) {
		v.AddError(param, v.messages.InvalidPublicID(param))
		return ""
	}
	return raw
}

// Int64sQuery extracts a comma-separated list of int64 values from a query parameter.
// Returns nil if the parameter is absent.
func (v *Validator) Int64sQuery(r *http.Request, param string) []int64 {
	raw := r.URL.Query().Get(param)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			v.AddError(param, v.messages.InvalidIntegerList(param))
			return nil
		}
		result = append(result, n)
	}
	return result
}

func isValidPublicID(s string) bool {
	if _, err := uuid.Parse(s); err == nil {
		return true
	}
	return isValidULID(s)
}

func isValidULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, c := range s {
		if !isCrockfordBase32(c) {
			return false
		}
	}
	return true
}

func isCrockfordBase32(c rune) bool {
	if c >= '0' && c <= '9' {
		return true
	}
	c = unicode.ToUpper(c)
	// Crockford base32 excludes: I, L, O, U
	return c >= 'A' && c <= 'Z' && c != 'I' && c != 'L' && c != 'O' && c != 'U'
}
