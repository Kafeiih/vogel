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

// TestDefaultMessages_ExactTexts pins the current English wording so a future
// refactor cannot silently change it for consumers who never call
// WithMessages.
func TestDefaultMessages_ExactTexts(t *testing.T) {
	m := request.DefaultMessages()

	assert.Equal(t, "Malformed JSON at position 5", m.MalformedJSON(5))
	assert.Equal(t, "Request body exceeds the 1MB limit", m.BodyTooLarge(1))
	assert.Equal(t, "flag must be a boolean", m.InvalidBoolean("flag"))
	assert.Equal(t, "amount must be a valid decimal", m.InvalidDecimal("amount"))
}

// TestWithMessages_OverridesDecodeMessage verifies a decode-path message
// (EmptyBody) can be localized through WithMessages.
func TestWithMessages_OverridesDecodeMessage(t *testing.T) {
	d := request.New(request.WithMessages(request.Messages{
		EmptyBody: "custom empty body message",
	}))

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	w := httptest.NewRecorder()

	var dst testPayload
	err := d.JSON(w, r, &dst)
	require.Error(t, err)

	var body errorEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "custom empty body message", body.Message)
}

// TestWithMessages_OverridesValidatorMessage verifies a Validator message
// (Required) can be localized through WithMessages, via Decoder.NewValidator.
func TestWithMessages_OverridesValidatorMessage(t *testing.T) {
	d := request.New(request.WithMessages(request.Messages{
		Required: func(field string) string { return "custom: " + field + " missing" },
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	v := d.NewValidator()
	v.UUIDParam(r, "id")

	assert.Equal(t, "custom: id missing", v.Errors()["id"])
}

// TestWithMessages_EmptyFieldFallsBackToDefault verifies the WithAuthMessages
// merge rule: only fields explicitly set in the override replace the
// default, every other field keeps DefaultMessages' English text.
func TestWithMessages_EmptyFieldFallsBackToDefault(t *testing.T) {
	d := request.New(request.WithMessages(request.Messages{
		Required: func(field string) string { return "custom: " + field },
	}))

	v := d.NewValidator()
	v.Enum("status", "bogus", []string{"active"})

	assert.Equal(t, "status must be one of the allowed values", v.Errors()["status"],
		"a message not set in WithMessages should keep the English default")
}

// TestValidator_AddError verifies AddError entries show up both in Errors()
// and in the WriteErrors wire response, since AddError is the supported way
// for consumers embedding Validator in their own parsers to report errors.
func TestValidator_AddError(t *testing.T) {
	v := request.NewValidator()
	v.AddError("custom_field", "custom message")

	assert.Equal(t, "custom message", v.Errors()["custom_field"])
	assert.True(t, v.HasErrors())

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	v.WriteErrors(w, r)

	var body struct {
		Fields map[string]string `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "custom message", body.Fields["custom_field"])
}

// TestPackageLevelJSON_MatchesNewDecoderDefaultBytes verifies that a fresh
// request.New() behaves exactly like the package-level functions. It does not
// prove compatibility with v0.3.0 on its own — both sides share
// DefaultMessages — that is what the golden tests in compat_test.go pin.
func TestPackageLevelJSON_MatchesNewDecoderDefaultBytes(t *testing.T) {
	body := `{this is not json}`

	r1 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w1 := httptest.NewRecorder()
	var dst1 testPayload
	err1 := request.JSON(w1, r1, &dst1)

	r2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w2 := httptest.NewRecorder()
	var dst2 testPayload
	err2 := request.New().JSON(w2, r2, &dst2)

	require.Error(t, err1)
	require.Error(t, err2)
	assert.Equal(t, w1.Code, w2.Code)
	assert.Equal(t, w1.Body.Bytes(), w2.Body.Bytes())
}

// TestValidator_AddError_LastWriteWins pins the documented overwrite rule, for
// both a repeated AddError and an AddError after one of the package's own
// parsers already recorded an error for that field — the case a consumer
// mixing its own parsers with Validator's hits.
func TestValidator_AddError_LastWriteWins(t *testing.T) {
	t.Run("repeated AddError", func(t *testing.T) {
		v := request.NewValidator()
		v.AddError("amount", "first")
		v.AddError("amount", "second")

		assert.Equal(t, request.FieldErrors{"amount": "second"}, v.Errors())
	})

	t.Run("AddError after a built-in parser", func(t *testing.T) {
		v := request.NewValidator()
		v.IntQuery(httptest.NewRequest(http.MethodGet, "/?limit=abc", nil), "limit", 10)
		require.Equal(t, "limit must be an integer", v.Errors()["limit"])

		v.AddError("limit", "limit must be at most 100")

		assert.Equal(t, request.FieldErrors{"limit": "limit must be at most 100"}, v.Errors())
	})
}
