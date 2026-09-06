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
	errors FieldErrors
}

// NewValidator creates a ready-to-use Validator.
func NewValidator() *Validator {
	return &Validator{errors: make(FieldErrors)}
}

// HasErrors reports whether any validation errors have been recorded.
func (v *Validator) HasErrors() bool {
	return len(v.errors) > 0
}

// Errors returns the accumulated field errors.
func (v *Validator) Errors() FieldErrors {
	return v.errors
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
		v.errors[param] = param + " is required"
		return uuid.Nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		v.errors[param] = param + " must be a valid UUID"
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
		v.errors[param] = param + " must be an integer"
		return defaultVal
	}
	if n < 0 {
		v.errors[param] = param + " must be non-negative"
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
		v.errors[param] = param + " must be in RFC3339 format"
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
		v.errors[param] = param + " must have ISO format (YYYY-MM-DD)"
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
		v.errors[param] = param + " must be a valid UUID"
		return nil
	}
	return &id
}

// Int64Param extracts and validates a chi URL parameter as an int64.
// Returns 0 if validation fails.
func (v *Validator) Int64Param(r *http.Request, param string) int64 {
	raw := chi.URLParam(r, param)
	if raw == "" {
		v.errors[param] = param + " is required"
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		v.errors[param] = param + " must be a valid integer"
		return 0
	}
	return n
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
	v.errors[param] = param + " must be one of the allowed values"
	return value
}

// PublicIDParam extracts and validates a chi URL parameter as a UUID or ULID.
// Returns the raw string unchanged. Returns "" if validation fails.
func (v *Validator) PublicIDParam(r *http.Request, param string) string {
	raw := chi.URLParam(r, param)
	if raw == "" {
		v.errors[param] = param + " is required"
		return ""
	}
	if !isValidPublicID(raw) {
		v.errors[param] = param + " must be a valid UUID or ULID"
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
			v.errors[param] = param + " must contain valid integers"
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
