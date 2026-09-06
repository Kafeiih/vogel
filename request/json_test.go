package request_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/request"
)

type testPayload struct {
	Name string `json:"name"`
}

// TestJSONWithLimit_ValidSmallPayload verifies that a small valid payload is decoded
// without error and the target struct is populated.
func TestJSONWithLimit_ValidSmallPayload(t *testing.T) {
	body := `{"name":"hello"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONWithLimit(w, r, &dst, 1024)
	require.NoError(t, err)
	assert.Equal(t, "hello", dst.Name)
}

// TestJSONWithLimit_PayloadAtExactLimit verifies that a payload at exactly the limit
// is accepted.
func TestJSONWithLimit_PayloadAtExactLimit(t *testing.T) {
	const maxBytes = 1024
	// {"name":"<padding>"} — pad Name to hit exactly maxBytes total.
	padding := strings.Repeat("a", maxBytes-len(`{"name":""}`))
	body := `{"name":"` + padding + `"}`
	require.Equal(t, maxBytes, len(body), "body must be exactly maxBytes")

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONWithLimit(w, r, &dst, maxBytes)
	require.NoError(t, err)
}

// TestJSONWithLimit_PayloadExceedsLimit verifies that a payload larger than maxBytes
// triggers an error and a 413 response.
func TestJSONWithLimit_PayloadExceedsLimit(t *testing.T) {
	const maxBytes = 512
	body := strings.Repeat("a", maxBytes+100)
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"`+body+`"}`))
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONWithLimit(w, r, &dst, maxBytes)
	require.Error(t, err, "payload exceeding limit should return an error")
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

// TestJSONWithLimit_MalformedJSON verifies that invalid JSON within limit returns
// a non-nil error and a 400 response.
func TestJSONWithLimit_MalformedJSON(t *testing.T) {
	body := `{this is not json}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONWithLimit(w, r, &dst, 4096)
	require.Error(t, err, "malformed JSON should return an error")
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestJSONWithLimit_UnknownField verifies that unknown fields trigger a decode error
// because DisallowUnknownFields is active.
func TestJSONWithLimit_UnknownField(t *testing.T) {
	body := `{"name":"ok","unknown_field":"value"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONWithLimit(w, r, &dst, 4096)
	require.Error(t, err, "unknown field should return an error")
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestJSONWithLimit_EmptyBody verifies that an empty body returns a non-nil error.
func TestJSONWithLimit_EmptyBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(nil))
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONWithLimit(w, r, &dst, 4096)
	require.Error(t, err, "empty body should return an error")
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestJSON_DefaultLimitIsExactly1MiB verifies FIX 1: the default byte limit is exactly
// 1_048_576 (1 MiB), not the 1_048_578 off-by-two value the source repositories carried.
// A body of exactly 1 MiB must be accepted; one byte over must be rejected.
func TestJSON_DefaultLimitIsExactly1MiB(t *testing.T) {
	const oneMiB = 1024 * 1024

	t.Run("exactly 1MiB is accepted", func(t *testing.T) {
		padding := strings.Repeat("x", oneMiB-len(`{"name":""}`))
		body := `{"name":"` + padding + `"}`
		require.Equal(t, oneMiB, len(body))

		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.Body = io.NopCloser(strings.NewReader(body))
		w := httptest.NewRecorder()

		var dst testPayload
		err := request.JSON(w, r, &dst)
		require.NoError(t, err, "exactly 1MiB should be accepted")
	})

	t.Run("1MiB plus one byte is rejected", func(t *testing.T) {
		padding := strings.Repeat("x", oneMiB-len(`{"name":""}`)+1)
		body := `{"name":"` + padding + `"}`
		require.Equal(t, oneMiB+1, len(body))

		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		w := httptest.NewRecorder()

		var dst testPayload
		err := request.JSON(w, r, &dst)
		require.Error(t, err, "1MiB+1 should be rejected")
		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	})
}

// TestJSONWithLimit_10MBBoundaryAccepted verifies that a body of exactly 10MB is accepted
// when the caller raises the limit via JSONWithLimit.
func TestJSONWithLimit_10MBBoundaryAccepted(t *testing.T) {
	const tenMB = 10 * 1024 * 1024
	padding := strings.Repeat("x", tenMB-len(`{"name":""}`))
	body := `{"name":"` + padding + `"}`
	require.Equal(t, tenMB, len(body))

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Body = io.NopCloser(strings.NewReader(body))
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONWithLimit(w, r, &dst, tenMB)
	require.NoError(t, err, "exactly 10MB should be accepted")
}

// TestJSONWithLimit_11MBRejected verifies that a body larger than the configured
// limit is rejected.
func TestJSONWithLimit_11MBRejected(t *testing.T) {
	const tenMB = 10 * 1024 * 1024
	const elevenMB = 11 * 1024 * 1024

	padding := strings.Repeat("x", elevenMB-len(`{"name":""}`))
	body := `{"name":"` + padding + `"}`

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONWithLimit(w, r, &dst, tenMB)
	require.Error(t, err, "11MB payload should be rejected")
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}
