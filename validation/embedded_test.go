package validation_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/validation"
)

// embeddedBase is reused across the tests below: an anonymous field with no
// json tag of its own, exactly the shape encoding/json flattens into its
// parent when marshaling/unmarshaling.
type embeddedBase struct {
	ID string `json:"id" validate:"required"`
}

// TestStruct_EmbeddedUntagged_Flattened verifies an anonymous struct field
// with no json tag contributes no segment of its own: the key must be "id",
// never "Base.id" or "base.id" — go-playground/validator's Namespace()
// reports the embedded field under its Go name ("Base", via
// RegisterTagNameFunc's untagged-field fallback), which this package must
// not leak.
func TestStruct_EmbeddedUntagged_Flattened(t *testing.T) {
	type In struct {
		embeddedBase
		Name string `json:"name" validate:"required"`
	}

	fields, err := validation.Struct(In{})
	require.NoError(t, err)
	require.NotNil(t, fields)

	assert.Contains(t, fields, "id")
	assert.Contains(t, fields, "name")
	assert.Equal(t, "id is required", fields["id"])
	for key, msg := range fields {
		assert.NotContains(t, key, "embeddedBase")
		assert.NotContains(t, key, "Base")
		assert.NotContains(t, msg, "embeddedBase")
		assert.NotContains(t, msg, "Base")
	}
}

// TestStruct_EmbeddedPointer_Flattened mirrors
// TestStruct_EmbeddedUntagged_Flattened for a *pointer* embed, which must
// flatten identically once the pointer itself is non-nil.
func TestStruct_EmbeddedPointer_Flattened(t *testing.T) {
	type InPtr struct {
		*embeddedBase
		Name string `json:"name" validate:"required"`
	}

	fields, err := validation.Struct(&InPtr{embeddedBase: &embeddedBase{}})
	require.NoError(t, err)
	require.NotNil(t, fields)

	assert.Contains(t, fields, "id")
	assert.Contains(t, fields, "name")
	for key := range fields {
		assert.NotContains(t, key, "embeddedBase")
		assert.NotContains(t, key, "Base")
	}
}

// TestStruct_EmbeddedWithJSONTag_NotFlattened verifies the mirror-image rule:
// an anonymous field that DOES carry an explicit json tag is not flattened —
// encoding/json itself would treat it as an ordinary named field, and so
// must this package.
func TestStruct_EmbeddedWithJSONTag_NotFlattened(t *testing.T) {
	type InTagged struct {
		embeddedBase `json:"base"`
		Name         string `json:"name" validate:"required"`
	}

	fields, err := validation.Struct(InTagged{})
	require.NoError(t, err)
	require.NotNil(t, fields)

	assert.Contains(t, fields, "base.id")
	assert.Contains(t, fields, "name")
	assert.NotContains(t, fields, "id")
}

// TestStruct_EmbeddedInNestedStruct_Flattened verifies flattening still
// applies when the embedding struct itself sits inside another nested
// field, not just at the validated root.
func TestStruct_EmbeddedInNestedStruct_Flattened(t *testing.T) {
	type In struct {
		embeddedBase
		Name string `json:"name" validate:"required"`
	}
	type Wrapper struct {
		Nested In `json:"nested"`
	}

	fields, err := validation.Struct(Wrapper{})
	require.NoError(t, err)
	require.NotNil(t, fields)

	assert.Contains(t, fields, "nested.id")
	assert.Contains(t, fields, "nested.name")
	for key := range fields {
		assert.NotContains(t, key, "embeddedBase")
		assert.NotContains(t, key, "Base")
	}
}

// TestStruct_EmbeddedInDiveSliceElement_Flattened verifies flattening also
// applies to an embedded field inside a struct reached through "dive".
func TestStruct_EmbeddedInDiveSliceElement_Flattened(t *testing.T) {
	type Elem struct {
		embeddedBase
		Label string `json:"label" validate:"required"`
	}
	type SliceHolder struct {
		Items []Elem `json:"items" validate:"dive"`
	}

	fields, err := validation.Struct(SliceHolder{Items: []Elem{{}}})
	require.NoError(t, err)
	require.NotNil(t, fields)

	assert.Contains(t, fields, "items[0].id")
	assert.Contains(t, fields, "items[0].label")
	for key := range fields {
		assert.NotContains(t, key, "embeddedBase")
		assert.NotContains(t, key, "Base")
	}
}

// TestStruct_TypedNilPointer verifies a typed nil pointer — valid Go, but
// nothing validator.Struct can walk — is treated as the programming error it
// is (see TestStruct_NonStructInput), never a panic and never a silent
// success.
func TestStruct_TypedNilPointer(t *testing.T) {
	type S struct {
		Name string `json:"name" validate:"required"`
	}

	var s *S
	fields, err := validation.Struct(s)
	assert.Error(t, err)
	assert.Nil(t, fields)
	assert.False(t, strings.Contains(err.Error(), "\x00"), "sanity: err.Error() must not panic-format")
}
