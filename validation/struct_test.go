package validation_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/validation"
)

// TestStruct_Valid verifies a struct that satisfies every tag returns
// (nil, nil): no field errors, no programming error.
func TestStruct_Valid(t *testing.T) {
	type S struct {
		Name string `json:"name" validate:"required"`
	}

	fields, err := validation.Struct(S{Name: "ok"})
	assert.NoError(t, err)
	assert.Nil(t, fields)
}

// TestStruct_KeyNaming covers every way a Go field name can map to a JSON
// key: an explicit json tag, json:"-", no tag at all, and a tag carrying the
// omitempty suffix (which must be stripped, keeping only the name before the
// first comma).
func TestStruct_KeyNaming(t *testing.T) {
	type S struct {
		Tagged    string `json:"tagged_name" validate:"required"`
		Dashed    string `json:"-" validate:"required"`
		Untagged  string `validate:"required"`
		OmitEmpty string `json:"omit_name,omitempty" validate:"required"`
	}

	fields, err := validation.Struct(S{})
	require.NoError(t, err)
	require.NotNil(t, fields)

	assert.Contains(t, fields, "tagged_name")
	assert.Contains(t, fields, "Dashed")
	assert.Contains(t, fields, "Untagged")
	assert.Contains(t, fields, "omit_name")
	assert.Len(t, fields, 4)
}

// TestStruct_NestedPath verifies a nested struct field's key is the JSON
// path joined with a dot, without the root struct's own name.
func TestStruct_NestedPath(t *testing.T) {
	type Address struct {
		Street string `json:"street" validate:"required"`
	}
	type S struct {
		Address Address `json:"address"`
	}

	fields, err := validation.Struct(S{})
	require.NoError(t, err)
	require.NotNil(t, fields)
	assert.Contains(t, fields, "address.street")
}

// TestStruct_DiveSlicePath verifies a dive into a slice of structs produces
// an indexed path, e.g. items[0].monto.
func TestStruct_DiveSlicePath(t *testing.T) {
	type Item struct {
		Monto float64 `json:"monto" validate:"required"`
	}
	type S struct {
		Items []Item `json:"items" validate:"dive"`
	}

	fields, err := validation.Struct(S{Items: []Item{{Monto: 0}}})
	require.NoError(t, err)
	require.NotNil(t, fields)
	assert.Contains(t, fields, "items[0].monto")
}

// TestStruct_PointerField verifies a nil pointer field trips "required" and
// that a present-but-out-of-range pointer field's Kind is reported as the
// pointee's kind (int), not reflect.Ptr, so Min/Max render numeric wording
// instead of falling through to a pointer-shaped default.
func TestStruct_PointerField(t *testing.T) {
	type S struct {
		Required *string `json:"required_ptr" validate:"required"`
		Age      *int    `json:"age" validate:"min=1"`
	}
	zero := 0

	fields, err := validation.Struct(S{Age: &zero})
	require.NoError(t, err)
	require.NotNil(t, fields)
	assert.Equal(t, "required_ptr is required", fields["required_ptr"])
	assert.Equal(t, "age must be at least 1", fields["age"])
}

// TestStruct_DefaultFallbackForOtherTag verifies a tag with no dedicated
// Messages field (e.g. "gt") falls back to Messages.Default instead of
// leaking the tag name or the Go error text.
func TestStruct_DefaultFallbackForOtherTag(t *testing.T) {
	type S struct {
		Age int `json:"age" validate:"gt=0"`
	}

	fields, err := validation.Struct(S{Age: -1})
	require.NoError(t, err)
	require.NotNil(t, fields)
	assert.Equal(t, "age is invalid", fields["age"])
}

// TestStruct_OneOf verifies the oneof tag uses Messages.OneOf.
func TestStruct_OneOf(t *testing.T) {
	type S struct {
		Status string `json:"status" validate:"oneof=draft sent paid"`
	}

	fields, err := validation.Struct(S{Status: "archived"})
	require.NoError(t, err)
	require.NotNil(t, fields)
	assert.Equal(t, "status must be one of: draft, sent, paid", fields["status"])
}

// TestStruct_NonStructInput verifies a non-struct/nil input returns a
// programming error, never a client-facing FieldErrors map — this is
// validator.InvalidValidationError, not a validation failure.
func TestStruct_NonStructInput(t *testing.T) {
	fields, err := validation.Struct(42)
	assert.Error(t, err)
	assert.Nil(t, fields)
}

// TestStruct_NilInput verifies a nil interface also returns a programming
// error rather than panicking or silently succeeding.
func TestStruct_NilInput(t *testing.T) {
	fields, err := validation.Struct(nil)
	assert.Error(t, err)
	assert.Nil(t, fields)
}
