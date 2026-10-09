package request_test

import (
	"encoding/json"
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

// jsonRequest builds a POST request carrying body under the
// application/json Content-Type that every decoder entry point requires by
// default. Tests about the Content-Type check itself build their own request.
func jsonRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// TestJSON_ContentType pins the media-type gate: only application/json and
// application/*+json (parameters allowed) reach the decoder; anything else,
// including a missing header, is a 415 with UNSUPPORTED_MEDIA_TYPE and the
// body is never decoded.
func TestJSON_ContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		noHeader    bool
		wantStatus  int
	}{
		{name: "missing header", noHeader: true, wantStatus: http.StatusUnsupportedMediaType},
		{name: "empty header value", contentType: "", wantStatus: http.StatusUnsupportedMediaType},
		{name: "text/plain", contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
		{name: "form encoded", contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusUnsupportedMediaType},
		{name: "json suffix on a non-application type", contentType: "text/vnd.foo+json", wantStatus: http.StatusUnsupportedMediaType},
		{name: "unparseable header", contentType: "application/", wantStatus: http.StatusUnsupportedMediaType},
		{name: "application/json", contentType: "application/json", wantStatus: http.StatusOK},
		{name: "application/json with charset", contentType: "application/json; charset=utf-8", wantStatus: http.StatusOK},
		{name: "application/json in upper case", contentType: "Application/JSON", wantStatus: http.StatusOK},
		{name: "application/vnd.api+json", contentType: "application/vnd.api+json", wantStatus: http.StatusOK},
		{name: "application/problem+json with charset", contentType: "application/problem+json; charset=utf-8", wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"hello"}`))
			if !tt.noHeader {
				r.Header.Set("Content-Type", tt.contentType)
			}
			w := httptest.NewRecorder()

			var dst testPayload
			err := request.JSON(w, r, &dst)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusOK {
				require.NoError(t, err)
				assert.Equal(t, "hello", dst.Name)
				assert.Equal(t, 0, w.Body.Len())
				return
			}

			require.Error(t, err)
			assert.Equal(t, "", dst.Name, "a rejected body must not be decoded")
			var body errorEnvelope
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, "UNSUPPORTED_MEDIA_TYPE", body.Code)
			assert.Equal(t, "Content-Type must be application/json", body.Message)
		})
	}
}

// TestJSON_TrailingData pins that exactly one JSON value is accepted: a second
// value, or any non-whitespace bytes after the first one, is a 400 with
// INVALID_JSON, even though the first value decoded fine.
func TestJSON_TrailingData(t *testing.T) {
	type payload struct {
		A int `json:"a"`
	}

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "second JSON value", body: `{"a":1} {"b":2}`, wantStatus: http.StatusBadRequest},
		{name: "garbage after the value", body: `{"a":1}garbage`, wantStatus: http.StatusBadRequest},
		{name: "second value on a new line", body: "{\"a\":1}\n{\"a\":2}", wantStatus: http.StatusBadRequest},
		{name: "null after the value", body: `{"a":1} null`, wantStatus: http.StatusBadRequest},
		{name: "trailing whitespace is fine", body: "{\"a\":1} \n\t", wantStatus: http.StatusOK},
		{name: "single value", body: `{"a":1}`, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()

			var dst payload
			err := request.JSON(w, jsonRequest(tt.body), &dst)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusOK {
				require.NoError(t, err)
				assert.Equal(t, 1, dst.A)
				return
			}

			require.Error(t, err)
			var body errorEnvelope
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, "INVALID_JSON", body.Code)
			assert.Equal(t, "Request body contains data after the JSON value", body.Message)
		})
	}
}

// TestJSONWithLimit_TrailingDataAndTheLimit pins how the single-value rule and
// the byte limit interact. Garbage the decoder already buffered is reported
// as trailing data; when the decoder has to read past the limit to find the
// end of the body, the limit is what fails and the client gets the 413 that
// names the real problem.
func TestJSONWithLimit_TrailingDataAndTheLimit(t *testing.T) {
	const maxBytes = 64

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "garbage within the limit", body: `{"name":"a"}` + strings.Repeat("x", maxBytes), wantStatus: http.StatusBadRequest},
		{name: "whitespace past the limit", body: `{"name":"a"}` + strings.Repeat(" ", 2*maxBytes), wantStatus: http.StatusRequestEntityTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()

			var dst testPayload
			err := request.JSONWithLimit(w, jsonRequest(tt.body), &dst, maxBytes)

			require.Error(t, err)
			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

// TestJSONWithLimit_ValidSmallPayload verifies that a small valid payload is decoded
// without error and the target struct is populated.
func TestJSONWithLimit_ValidSmallPayload(t *testing.T) {
	body := `{"name":"hello"}`
	r := jsonRequest(body)
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

	r := jsonRequest(body)
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
	r := jsonRequest(`{"name":"` + body + `"}`)
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
	r := jsonRequest(body)
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
	r := jsonRequest(body)
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONWithLimit(w, r, &dst, 4096)
	require.Error(t, err, "unknown field should return an error")
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestJSONWithLimit_EmptyBody verifies that an empty body returns a non-nil error.
func TestJSONWithLimit_EmptyBody(t *testing.T) {
	r := jsonRequest("")
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

		r := jsonRequest(body)
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

		r := jsonRequest(body)
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

	r := jsonRequest(body)
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

	r := jsonRequest(body)
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONWithLimit(w, r, &dst, tenMB)
	require.Error(t, err, "11MB payload should be rejected")
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}
