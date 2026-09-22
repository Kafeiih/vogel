package validation_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/validation"
)

type writeTestPayload struct {
	Name string `json:"name" validate:"required"`
}

// TestWrite_Valid verifies a valid value returns true and writes nothing to
// the response.
func TestWrite_Valid(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	ok := validation.Write(w, r, &writeTestPayload{Name: "ok"})

	assert.True(t, ok)
	assert.Equal(t, 200, w.Code, "Write must not touch the status when valid")
	assert.Empty(t, w.Body.String())
}

// TestWrite_Invalid verifies an invalid value returns false and writes
// exactly what response.ValidationError would write, decoded and compared
// field by field rather than assuming a byte-identical dump.
func TestWrite_Invalid(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	ok := validation.Write(w, r, &writeTestPayload{})

	assert.False(t, ok)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Build the reference response the same way response.ValidationError
	// does, and compare the decoded bodies instead of raw bytes.
	wantRec := httptest.NewRecorder()
	response.ValidationError(wantRec, r, map[string]string{"name": "name is required"})

	var got, want response.ValidationErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	require.NoError(t, json.NewDecoder(wantRec.Body).Decode(&want))
	assert.Equal(t, want, got)
}

// TestWrite_ProgrammingError verifies a non-struct/nil input — a bug in the
// caller, not a client validation failure — returns false, writes 500, and
// never leaks the underlying Go error text into the response body.
func TestWrite_ProgrammingError(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	var notAStruct int
	ok := validation.Write(w, r, notAStruct)

	assert.False(t, ok)
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	body := w.Body.String()
	assert.NotContains(t, strings.ToLower(body), "invalidvalidationerror")
	assert.NotContains(t, strings.ToLower(body), "reflect")

	var got response.ErrorResponse
	require.NoError(t, json.NewDecoder(strings.NewReader(body)).Decode(&got))
	assert.Equal(t, response.CodeInternalError, got.Code)
	assert.NotEmpty(t, got.Message)
}

// TestPackageLevel_StructAndWrite verifies the package-level Struct and
// Write helpers delegate to a default instance, exactly like request's
// package-level functions delegate to defaultDecoder.
func TestPackageLevel_StructAndWrite(t *testing.T) {
	fields, err := validation.Struct(&writeTestPayload{})
	require.NoError(t, err)
	assert.Equal(t, "name is required", fields["name"])

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	ok := validation.Write(w, r, &writeTestPayload{Name: "ok"})
	assert.True(t, ok)
}
