package validation_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kafeiih/vogel/validation"
)

// TestDefaultMessages_Required verifies the default wording for a missing
// required field, matching request's field-key-first style.
func TestDefaultMessages_Required(t *testing.T) {
	msgs := validation.DefaultMessages()
	assert.Equal(t, "amount is required", msgs.Required("amount"))
}

// TestDefaultMessages_MinByKind verifies the default Min wording changes with
// the field's kind, since "at least 3" means a different thing for a string,
// a collection and a number.
func TestDefaultMessages_MinByKind(t *testing.T) {
	msgs := validation.DefaultMessages()

	tests := []struct {
		name string
		kind reflect.Kind
		want string
	}{
		{"string", reflect.String, "name must be at least 3 characters long"},
		{"slice", reflect.Slice, "name must contain at least 3 items"},
		{"array", reflect.Array, "name must contain at least 3 items"},
		{"map", reflect.Map, "name must contain at least 3 items"},
		{"int", reflect.Int, "name must be at least 3"},
		{"int64", reflect.Int64, "name must be at least 3"},
		{"uint", reflect.Uint, "name must be at least 3"},
		{"float64", reflect.Float64, "name must be at least 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, msgs.Min("name", "3", tt.kind))
		})
	}
}

// TestDefaultMessages_MaxByKind mirrors TestDefaultMessages_MinByKind for Max.
func TestDefaultMessages_MaxByKind(t *testing.T) {
	msgs := validation.DefaultMessages()

	tests := []struct {
		name string
		kind reflect.Kind
		want string
	}{
		{"string", reflect.String, "name must be at most 10 characters long"},
		{"slice", reflect.Slice, "name must contain at most 10 items"},
		{"map", reflect.Map, "name must contain at most 10 items"},
		{"int", reflect.Int, "name must be at most 10"},
		{"float64", reflect.Float64, "name must be at most 10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, msgs.Max("name", "10", tt.kind))
		})
	}
}

// TestDefaultMessages_OneOf verifies oneof's param (space-separated, as
// go-playground/validator renders it) is rendered comma-separated for
// readability.
func TestDefaultMessages_OneOf(t *testing.T) {
	msgs := validation.DefaultMessages()
	assert.Equal(t, "status must be one of: draft, sent, paid", msgs.OneOf("status", "draft sent paid"))
}

// TestDefaultMessages_Default verifies the fallback message for a tag with no
// dedicated Messages field never leaks the raw tag name.
func TestDefaultMessages_Default(t *testing.T) {
	msgs := validation.DefaultMessages()
	assert.Equal(t, "email is invalid", msgs.Default("email", "email"))
}

// TestWithMessages_OverridesOnlyGivenFields verifies WithMessages follows the
// same merge rule as request.WithMessages: a field left nil keeps its
// DefaultMessages value, so a caller only needs to set what it wants to
// localize.
func TestWithMessages_OverridesOnlyGivenFields(t *testing.T) {
	v := validation.New(validation.WithMessages(validation.Messages{
		Required: func(field string) string { return field + " es obligatorio" },
	}))

	type S struct {
		Name string `json:"name" validate:"required"`
		Age  int    `json:"age" validate:"min=1"`
	}

	fields, err := v.Struct(S{})
	assert.NoError(t, err)
	assert.Equal(t, "name es obligatorio", fields["name"])
	// Age's message was never overridden, so it must still be the English
	// default coming from DefaultMessages, proving nil fields are not reset.
	assert.Equal(t, "age must be at least 1", fields["age"])
}
