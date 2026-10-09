package request_test

import (
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
// destination is left exactly as passed, so a handler can tell "the client
// sent nothing" apart from "the client sent something invalid".
func TestJSONOptional_EmptyBody(t *testing.T) {
	r := jsonRequest("")
	w := httptest.NewRecorder()

	// Pre-filled so a decoder that zeroed the destination would fail too.
	dst := testPayload{Name: "preset"}
	err := request.JSONOptional(w, r, &dst)

	require.NoError(t, err)
	assert.Equal(t, testPayload{Name: "preset"}, dst, "destination must stay untouched on an empty body")
	assert.Equal(t, 0, w.Body.Len(), "nothing should be written to the response for an empty optional body")
	assert.Equal(t, http.StatusOK, w.Code, "no status was written, so the recorder keeps its 200 default")
}

// TestJSONOptional_MalformedJSON verifies that a non-empty but invalid body
// is still an error, same as JSON — JSONOptional only special-cases an empty
// body, not a bad one.
func TestJSONOptional_MalformedJSON(t *testing.T) {
	r := jsonRequest(`{this is not json}`)
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
	r := jsonRequest(`{"name":"ok","bogus":1}`)
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
	r := jsonRequest(`{"name":"hello"}`)
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONOptional(w, r, &dst)

	require.NoError(t, err)
	assert.Equal(t, "hello", dst.Name)
}

// TestJSONOptional_WhitespaceOnlyBody documents that a body holding only
// whitespace counts as empty: json.Decoder skips it and reports io.EOF.
func TestJSONOptional_WhitespaceOnlyBody(t *testing.T) {
	r := jsonRequest(" \n\t ")
	w := httptest.NewRecorder()

	dst := testPayload{Name: "preset"}
	err := request.JSONOptional(w, r, &dst)

	require.NoError(t, err)
	assert.Equal(t, "preset", dst.Name)
	assert.Equal(t, 0, w.Body.Len())
}

// TestJSONOptional_TruncatedBody guards the boundary of the empty-body case:
// a body that starts a value and stops (io.ErrUnexpectedEOF, not io.EOF) is
// a bad body, not an absent one, and must still be a 400.
func TestJSONOptional_TruncatedBody(t *testing.T) {
	r := jsonRequest(`{`)
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONOptional(w, r, &dst)

	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var body errorEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "INVALID_JSON", body.Code)
}

// TestDecoderJSONOptional_UsesInstanceMessages verifies the Decoder method
// shares the empty-body rule and reports other errors with the instance's
// localized messages.
func TestDecoderJSONOptional_UsesInstanceMessages(t *testing.T) {
	d := request.New(request.WithMessages(request.Messages{
		UnknownField: func(field string) string { return "campo desconocido: " + field },
	}))

	t.Run("empty body is still a no-op", func(t *testing.T) {
		r := jsonRequest("")
		w := httptest.NewRecorder()

		var dst testPayload
		require.NoError(t, d.JSONOptional(w, r, &dst))
		assert.Equal(t, 0, w.Body.Len())
	})

	t.Run("errors use the localized message", func(t *testing.T) {
		r := jsonRequest(`{"bogus":1}`)
		w := httptest.NewRecorder()

		var dst testPayload
		require.Error(t, d.JSONOptional(w, r, &dst))

		var body errorEnvelope
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, "UNKNOWN_FIELD", body.Code)
		assert.Equal(t, `campo desconocido: "bogus"`, body.Message)
	})
}

// TestJSONOptional_ContentType pins how the optional body interacts with the
// media-type gate. An absent body is the one case a client legitimately sends
// no Content-Type for, so "no header + nothing to decode" stays a success;
// the moment there is something to decode, or the client names a non-JSON
// type, the request is a 415 exactly like JSON.
func TestJSONOptional_ContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		noHeader    bool
		body        string
		wantStatus  int
		wantErr     bool
		wantName    string
	}{
		{name: "no header, empty body", noHeader: true, body: ``, wantStatus: http.StatusOK, wantName: "preset"},
		{name: "no header, whitespace body", noHeader: true, body: " \n\t ", wantStatus: http.StatusOK, wantName: "preset"},
		{name: "no header, JSON body", noHeader: true, body: `{"name":"hello"}`, wantStatus: http.StatusUnsupportedMediaType, wantErr: true, wantName: "preset"},
		{name: "no header, garbage body", noHeader: true, body: `nope`, wantStatus: http.StatusUnsupportedMediaType, wantErr: true, wantName: "preset"},
		{name: "text/plain, empty body", contentType: "text/plain", body: ``, wantStatus: http.StatusUnsupportedMediaType, wantErr: true, wantName: "preset"},
		{name: "text/plain, JSON body", contentType: "text/plain", body: `{"name":"hello"}`, wantStatus: http.StatusUnsupportedMediaType, wantErr: true, wantName: "preset"},
		{name: "application/json, empty body", contentType: "application/json", body: ``, wantStatus: http.StatusOK, wantName: "preset"},
		{name: "application/json with charset, JSON body", contentType: "application/json; charset=utf-8", body: `{"name":"hello"}`, wantStatus: http.StatusOK, wantName: "hello"},
		{name: "application/vnd.api+json, JSON body", contentType: "application/vnd.api+json", body: `{"name":"hello"}`, wantStatus: http.StatusOK, wantName: "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			if !tt.noHeader {
				r.Header.Set("Content-Type", tt.contentType)
			}
			w := httptest.NewRecorder()

			dst := testPayload{Name: "preset"}
			err := request.JSONOptional(w, r, &dst)

			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Equal(t, tt.wantName, dst.Name)
			if !tt.wantErr {
				require.NoError(t, err)
				assert.Equal(t, 0, w.Body.Len())
				return
			}

			require.Error(t, err)
			var body errorEnvelope
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, "UNSUPPORTED_MEDIA_TYPE", body.Code)
		})
	}
}

// TestJSONOptional_TrailingData verifies the optional body shares the
// one-value rule with JSON.
func TestJSONOptional_TrailingData(t *testing.T) {
	w := httptest.NewRecorder()

	var dst testPayload
	err := request.JSONOptional(w, jsonRequest(`{"name":"a"} {"name":"b"}`), &dst)

	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var body errorEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "INVALID_JSON", body.Code)
}
