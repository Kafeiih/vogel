package request_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/request"
)

// errorEnvelope mirrors httpx/response.ErrorResponse for decoding in tests
// without importing the response package, keeping this test file focused on
// request's own contract (the code and message it produces).
type errorEnvelope struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

// TestJSONOptional_EmptyBody verifies the defining behavior of JSONOptional:
// an empty body is not an error. Nothing is written to the response and the
// destination is left at its zero value, so a handler can tell "the client
// sent nothing" apart from "the client sent something invalid".
func TestJSONOptional_EmptyBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(nil))
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONOptional(w, r, &dst)

	require.NoError(t, err)
	assert.Equal(t, testPayload{}, dst, "destination must stay untouched on an empty body")
	assert.Equal(t, 0, w.Body.Len(), "nothing should be written to the response for an empty optional body")
	assert.Equal(t, http.StatusOK, w.Code, "no status was written, so the recorder keeps its 200 default")
}

// TestJSONOptional_MalformedJSON verifies that a non-empty but invalid body
// is still an error, same as JSON — JSONOptional only special-cases an empty
// body, not a bad one.
func TestJSONOptional_MalformedJSON(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{this is not json}`))
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONOptional(w, r, &dst)

	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var body errorEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "INVALID_JSON", body.Code)
}

// TestJSONOptional_UnknownField verifies DisallowUnknownFields still applies.
func TestJSONOptional_UnknownField(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ok","bogus":1}`))
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONOptional(w, r, &dst)

	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var body errorEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "UNKNOWN_FIELD", body.Code)
}

// TestJSONOptional_ValidBody verifies a well-formed body decodes normally.
func TestJSONOptional_ValidBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"hello"}`))
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONOptional(w, r, &dst)

	require.NoError(t, err)
	assert.Equal(t, "hello", dst.Name)
}
