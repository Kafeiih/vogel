package validation

import (
	"fmt"
	"reflect"
	"strings"
)

// Messages holds every user-facing string Struct can produce, one function
// per go-playground/validator tag this package gives dedicated wording to,
// plus a Default fallback for any other tag.
//
// A library must not bake user-facing copy in one language: DefaultMessages
// returns neutral English defaults, and a consuming application configures
// its own copy once, via New(WithMessages(...)).
type Messages struct {
	// Required is used when a field fails the "required" tag.
	Required func(field string) string

	// Min is used when a field fails the "min" tag. param is the tag's
	// argument, exactly as go-playground/validator reports it via
	// FieldError.Param(). kind is the field's reflect.Kind after
	// dereferencing pointers (FieldError.Kind() already does this), because
	// "at least 3" means a different thing for a string (characters), a
	// slice/array/map (items) and a number (its value).
	Min func(field, param string, kind reflect.Kind) string

	// Max mirrors Min for the "max" tag.
	Max func(field, param string, kind reflect.Kind) string

	// OneOf is used when a field fails the "oneof" tag. param is the tag's
	// argument exactly as go-playground/validator reports it — the allowed
	// values separated by single spaces (e.g. "draft sent paid").
	OneOf func(field, param string) string

	// Default is used for any validator tag with no dedicated field above
	// (for example "email" or "gt"). tag is the failing tag's name
	// (FieldError.Tag()). The default wording deliberately does not
	// interpolate tag into the message: a validator tag name is
	// implementation detail, not something a client should see, even though
	// it is not a Go struct/field name.
	Default func(field, tag string) string
}

// DefaultMessages returns the neutral English defaults used when a Validator
// is built with no WithMessages option.
func DefaultMessages() Messages {
	return Messages{
		Required: func(field string) string { return field + " is required" },
		Min: func(field, param string, kind reflect.Kind) string {
			return minMaxMessage(field, param, kind, "at least")
		},
		Max: func(field, param string, kind reflect.Kind) string {
			return minMaxMessage(field, param, kind, "at most")
		},
		OneOf: func(field, param string) string {
			return field + " must be one of: " + strings.Join(strings.Fields(param), ", ")
		},
		Default: func(field, _ string) string { return field + " is invalid" },
	}
}

// minMaxMessage renders Min/Max's default wording. comparator is "at least"
// or "at most". The kind determines what unit the number means:
//   - a string's min/max bounds its length, in characters;
//   - a slice, array or map's min/max bounds how many items it holds;
//   - every other kind (every numeric kind go-playground/validator applies
//     min/max to, and anything else it might in the future) uses the plain
//     numeric wording, since there is no more specific unit to name.
func minMaxMessage(field, param string, kind reflect.Kind, comparator string) string {
	switch kind {
	case reflect.String:
		return fmt.Sprintf("%s must be %s %s characters long", field, comparator, param)
	case reflect.Slice, reflect.Array, reflect.Map:
		return fmt.Sprintf("%s must contain %s %s items", field, comparator, param)
	default:
		return fmt.Sprintf("%s must be %s %s", field, comparator, param)
	}
}

// Option customizes a Validator built with New.
type Option func(*Validator)

// WithMessages overrides the default user-facing messages. Any field left
// nil keeps the DefaultMessages value — the same merge rule as
// request.WithMessages — so a caller only needs to set the fields it wants
// to localize.
func WithMessages(m Messages) Option {
	return func(v *Validator) {
		if m.Required != nil {
			v.messages.Required = m.Required
		}
		if m.Min != nil {
			v.messages.Min = m.Min
		}
		if m.Max != nil {
			v.messages.Max = m.Max
		}
		if m.OneOf != nil {
			v.messages.OneOf = m.OneOf
		}
		if m.Default != nil {
			v.messages.Default = m.Default
		}
	}
}
