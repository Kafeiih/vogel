package request_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/request"
)

// The tests in this file pin the exact wire output of v0.3.0 for consumers
// that never call WithMessages. They deliberately use only package-level API
// that already existed in v0.3.0 (JSON, JSONWithLimit, NewValidator), so the
// same file compiles and passes against the v0.3.0 tag — that is what makes
// the expected bytes golden rather than a snapshot of the current code.

// TestJSON_DefaultResponsesAreByteIdenticalToV030 covers every branch of the
// decode error mapping with the literal response body v0.3.0 produced.
func TestJSON_DefaultResponsesAreByteIdenticalToV030(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		maxBytes   int64
		wantStatus int
		wantBody   string
	}{
		{
			name:       "malformed JSON",
			body:       `{this is not json}`,
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"success":false,"message":"Malformed JSON at position 2","code":"INVALID_JSON"}` + "\n",
		},
		{
			name:       "wrong type",
			body:       `{"name":1}`,
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"success":false,"message":"Wrong type for field \"name\": expected string","code":"INVALID_JSON"}` + "\n",
		},
		{
			name:       "body too large",
			body:       `{"name":"` + strings.Repeat("x", 2*1024*1024) + `"}`,
			maxBytes:   1024 * 1024,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   `{"success":false,"message":"Request body exceeds the 1MB limit","code":"BODY_TOO_LARGE"}` + "\n",
		},
		{
			name:       "empty body",
			body:       ``,
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"success":false,"message":"Request body is empty","code":"INVALID_JSON"}` + "\n",
		},
		{
			name:       "unknown field",
			body:       `{"bogus":1}`,
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"success":false,"message":"Unknown field: \"bogus\"","code":"UNKNOWN_FIELD"}` + "\n",
		},
		{
			name:       "truncated body falls through to the generic message",
			body:       `{"name":"a"`,
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"success":false,"message":"Invalid request body","code":"INVALID_JSON"}` + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			w := httptest.NewRecorder()

			var dst testPayload
			var err error
			if tt.maxBytes > 0 {
				err = request.JSONWithLimit(w, r, &dst, tt.maxBytes)
			} else {
				err = request.JSON(w, r, &dst)
			}

			require.Error(t, err)
			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
			assert.Equal(t, tt.wantBody, w.Body.String())
		})
	}
}

// TestValidator_DefaultMessagesMatchV030 pins every Validator message a
// default Validator records, by driving each parser into its failure branch.
func TestValidator_DefaultMessagesMatchV030(t *testing.T) {
	withParam := func(name, value string) *http.Request {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add(name, value)
		return requestWithRouteContext(httptest.NewRequest(http.MethodGet, "/", nil), rctx)
	}
	query := func(q string) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/?"+q, nil)
	}

	tests := []struct {
		name  string
		field string
		run   func(v *request.Validator)
		want  string
	}{
		{"required", "id", func(v *request.Validator) { v.UUIDParam(query(""), "id") }, "id is required"},
		{"uuid param", "id", func(v *request.Validator) { v.UUIDParam(withParam("id", "nope"), "id") }, "id must be a valid UUID"},
		{"uuid query", "id", func(v *request.Validator) { v.UUIDQuery(query("id=nope"), "id") }, "id must be a valid UUID"},
		{"integer", "limit", func(v *request.Validator) { v.IntQuery(query("limit=abc"), "limit", 1) }, "limit must be an integer"},
		{"non-negative", "limit", func(v *request.Validator) { v.IntQuery(query("limit=-1"), "limit", 1) }, "limit must be non-negative"},
		{"rfc3339", "since", func(v *request.Validator) { v.TimeQuery(query("since=nope"), "since") }, "since must be in RFC3339 format"},
		{"iso date", "date", func(v *request.Validator) { v.DateQuery(query("date=nope"), "date") }, "date must have ISO format (YYYY-MM-DD)"},
		{"int64 required", "n", func(v *request.Validator) { v.Int64Param(query(""), "n") }, "n is required"},
		{"int64 invalid", "n", func(v *request.Validator) { v.Int64Param(withParam("n", "x"), "n") }, "n must be a valid integer"},
		{"enum", "status", func(v *request.Validator) { v.Enum("status", "bogus", []string{"active"}) }, "status must be one of the allowed values"},
		{"public id required", "id", func(v *request.Validator) { v.PublicIDParam(query(""), "id") }, "id is required"},
		{"public id invalid", "id", func(v *request.Validator) { v.PublicIDParam(withParam("id", "nope"), "id") }, "id must be a valid UUID or ULID"},
		{"int64 list", "ids", func(v *request.Validator) { v.Int64sQuery(query("ids=1,x"), "ids") }, "ids must contain valid integers"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := request.NewValidator()
			tt.run(v)
			assert.Equal(t, request.FieldErrors{tt.field: tt.want}, v.Errors())
		})
	}
}
