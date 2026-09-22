package request

import "fmt"

// Messages holds every user-facing string this package can return, split
// into two groups: the decode-path messages a Decoder writes into a JSON
// error response (json.go), and the validation messages a Validator records
// per field (validate.go).
//
// A library must not bake user-facing copy in one language: what reads as a
// helpful English error to one deployment is the wrong language for another.
// DefaultMessages returns neutral English defaults; each consuming
// application configures its own copy once, via New(WithMessages(...)), and
// every handler built from that Decoder or its Validators uses it.
//
// There is deliberately no message for Validator.MaxInt: it only clamps a
// value to a maximum and never records a validation error, in this package
// and in every known fork of it. Adding a message field nothing would ever
// read is dead API.
type Messages struct {
	// MalformedJSON is used on HTTP 400 when the request body is not valid
	// JSON (a syntax error). offset is the byte offset from
	// encoding/json.SyntaxError where the parser gave up.
	MalformedJSON func(offset int64) string

	// WrongType is used on HTTP 400 when a field's JSON value does not match
	// its Go struct field's type. expected is
	// encoding/json.UnmarshalTypeError.Type.String().
	WrongType func(field, expected string) string

	// BodyTooLarge is used on HTTP 413 when the request body exceeds the
	// configured byte limit (defaultMaxBytes for JSON, or the caller-supplied
	// limit for JSONWithLimit). limitMB is that limit expressed in megabytes.
	BodyTooLarge func(limitMB float64) string

	// EmptyBody is used on HTTP 400 when JSON or JSONWithLimit receives an
	// empty request body. JSONOptional treats an empty body as success
	// instead, so this message never applies there.
	EmptyBody string

	// UnknownField is used on HTTP 400 when the body contains a field not
	// present in the destination struct (DisallowUnknownFields is always
	// on). field is the raw quoted field name as encoding/json reports it,
	// e.g. `"bogus"`.
	UnknownField func(field string) string

	// InvalidBody is used on HTTP 400 as the fallback for a decode error that
	// does not match any of the more specific cases above.
	InvalidBody string

	// Required is used by Validator methods that reject a missing/empty
	// value (UUIDParam, Int64Param, PublicIDParam), as part of a 400
	// validation-error response written by WriteErrors.
	Required func(field string) string

	// InvalidUUID is used by Validator.UUIDParam and Validator.UUIDQuery when
	// the value fails uuid.Parse.
	InvalidUUID func(field string) string

	// NotInteger is used by Validator.IntQuery when the value fails
	// strconv.Atoi.
	NotInteger func(field string) string

	// Negative is used by Validator.IntQuery when the parsed value is below
	// zero.
	Negative func(field string) string

	// InvalidRFC3339 is used by Validator.TimeQuery when the value fails
	// time.Parse(time.RFC3339, ...).
	InvalidRFC3339 func(field string) string

	// InvalidISODate is used by Validator.DateQuery when the value fails
	// time.Parse("2006-01-02", ...).
	InvalidISODate func(field string) string

	// InvalidInteger is used by Validator.Int64Param when the value fails
	// strconv.ParseInt.
	InvalidInteger func(field string) string

	// NotAllowed is used by Validator.Enum when a non-empty value is not one
	// of the allowed options.
	NotAllowed func(field string) string

	// InvalidPublicID is used by Validator.PublicIDParam when the value is
	// neither a valid UUID nor a valid ULID.
	InvalidPublicID func(field string) string

	// InvalidIntegerList is used by Validator.Int64sQuery when any
	// comma-separated entry fails strconv.ParseInt.
	InvalidIntegerList func(field string) string

	// InvalidBoolean is used by Validator.BoolQuery when the value fails
	// strconv.ParseBool.
	InvalidBoolean func(field string) string

	// InvalidDecimal is not read by anything in this package directly. It
	// exists so a decimal-parsing helper package outside request (for
	// example, decimalx) can report a validation failure with wording that
	// stays consistent with the rest of a Validator's messages, via
	// Validator.Messages().InvalidDecimal.
	InvalidDecimal func(field string) string
}

// DefaultMessages returns the neutral English defaults used when a Decoder
// is built with no WithMessages option. These are the exact texts this
// package has always returned, so building a Decoder via New() with no
// options produces byte-identical responses to the package-level functions.
func DefaultMessages() Messages {
	return Messages{
		MalformedJSON: func(offset int64) string {
			return fmt.Sprintf("Malformed JSON at position %d", offset)
		},
		WrongType: func(field, expected string) string {
			return fmt.Sprintf("Wrong type for field %q: expected %s", field, expected)
		},
		BodyTooLarge: func(limitMB float64) string {
			return fmt.Sprintf("Request body exceeds the %.0fMB limit", limitMB)
		},
		EmptyBody: "Request body is empty",
		UnknownField: func(field string) string {
			return fmt.Sprintf("Unknown field: %s", field)
		},
		InvalidBody: "Invalid request body",

		Required:           func(field string) string { return field + " is required" },
		InvalidUUID:        func(field string) string { return field + " must be a valid UUID" },
		NotInteger:         func(field string) string { return field + " must be an integer" },
		Negative:           func(field string) string { return field + " must be non-negative" },
		InvalidRFC3339:     func(field string) string { return field + " must be in RFC3339 format" },
		InvalidISODate:     func(field string) string { return field + " must have ISO format (YYYY-MM-DD)" },
		InvalidInteger:     func(field string) string { return field + " must be a valid integer" },
		NotAllowed:         func(field string) string { return field + " must be one of the allowed values" },
		InvalidPublicID:    func(field string) string { return field + " must be a valid UUID or ULID" },
		InvalidIntegerList: func(field string) string { return field + " must contain valid integers" },
		InvalidBoolean:     func(field string) string { return field + " must be a boolean" },
		InvalidDecimal:     func(field string) string { return field + " must be a valid decimal" },
	}
}

// Option customizes a Decoder built with New.
type Option func(*Decoder)

// WithMessages overrides the default user-facing messages. Any field left as
// its zero value (nil func, or "" for a plain string) keeps the
// DefaultMessages value, so a caller only needs to set the fields it wants
// to localize.
func WithMessages(m Messages) Option {
	return func(d *Decoder) {
		if m.MalformedJSON != nil {
			d.messages.MalformedJSON = m.MalformedJSON
		}
		if m.WrongType != nil {
			d.messages.WrongType = m.WrongType
		}
		if m.BodyTooLarge != nil {
			d.messages.BodyTooLarge = m.BodyTooLarge
		}
		if m.EmptyBody != "" {
			d.messages.EmptyBody = m.EmptyBody
		}
		if m.UnknownField != nil {
			d.messages.UnknownField = m.UnknownField
		}
		if m.InvalidBody != "" {
			d.messages.InvalidBody = m.InvalidBody
		}
		if m.Required != nil {
			d.messages.Required = m.Required
		}
		if m.InvalidUUID != nil {
			d.messages.InvalidUUID = m.InvalidUUID
		}
		if m.NotInteger != nil {
			d.messages.NotInteger = m.NotInteger
		}
		if m.Negative != nil {
			d.messages.Negative = m.Negative
		}
		if m.InvalidRFC3339 != nil {
			d.messages.InvalidRFC3339 = m.InvalidRFC3339
		}
		if m.InvalidISODate != nil {
			d.messages.InvalidISODate = m.InvalidISODate
		}
		if m.InvalidInteger != nil {
			d.messages.InvalidInteger = m.InvalidInteger
		}
		if m.NotAllowed != nil {
			d.messages.NotAllowed = m.NotAllowed
		}
		if m.InvalidPublicID != nil {
			d.messages.InvalidPublicID = m.InvalidPublicID
		}
		if m.InvalidIntegerList != nil {
			d.messages.InvalidIntegerList = m.InvalidIntegerList
		}
		if m.InvalidBoolean != nil {
			d.messages.InvalidBoolean = m.InvalidBoolean
		}
		if m.InvalidDecimal != nil {
			d.messages.InvalidDecimal = m.InvalidDecimal
		}
	}
}
