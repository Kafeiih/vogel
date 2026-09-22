package validation_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestStruct_RequiredIf verifies "required_if" — a conditional variant of
// "required" — renders through Messages.Required, exactly like plain
// "required", instead of falling back to Messages.Default and leaking the
// "required_if" tag name.
func TestStruct_RequiredIf(t *testing.T) {
	type S struct {
		Kind   string  `json:"kind"`
		Amount *string `json:"amount" validate:"required_if=Kind fixed"`
	}

	fields, err := validation.Struct(S{Kind: "fixed"})
	require.NoError(t, err)
	require.NotNil(t, fields)
	assert.Equal(t, "amount is required", fields["amount"])
}

// TestStruct_RequiredWith verifies "required_with" also renders through
// Messages.Required.
func TestStruct_RequiredWith(t *testing.T) {
	type S struct {
		A string `json:"a"`
		B string `json:"b" validate:"required_with=A"`
	}

	fields, err := validation.Struct(S{A: "x"})
	require.NoError(t, err)
	require.NotNil(t, fields)
	assert.Equal(t, "b is required", fields["b"])
}

// TestStruct_RequiredWithout verifies "required_without" also renders through
// Messages.Required.
func TestStruct_RequiredWithout(t *testing.T) {
	type S struct {
		A string `json:"a"`
		B string `json:"b" validate:"required_without=A"`
	}

	fields, err := validation.Struct(S{})
	require.NoError(t, err)
	require.NotNil(t, fields)
	assert.Equal(t, "b is required", fields["b"])
}

// TestStruct_RequiredIf_HonorsMessagesOverride verifies a WithMessages
// Required override applies to the conditional required_* tags exactly like
// it applies to plain "required".
func TestStruct_RequiredIf_HonorsMessagesOverride(t *testing.T) {
	v := validation.New(validation.WithMessages(validation.Messages{
		Required: func(field string) string { return field + " es obligatorio" },
	}))

	type S struct {
		Kind   string  `json:"kind"`
		Amount *string `json:"amount" validate:"required_if=Kind fixed"`
	}

	fields, err := v.Struct(S{Kind: "fixed"})
	require.NoError(t, err)
	require.NotNil(t, fields)
	assert.Equal(t, "amount es obligatorio", fields["amount"])
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
