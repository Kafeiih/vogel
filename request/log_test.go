package request_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/reqctx"
	"github.com/kafeiih/vogel/request"
)

// captureLogger returns a JSON logger writing to a buffer, and a func that
// decodes every record written so far.
func captureLogger(t *testing.T) (*slog.Logger, func() []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return l, func() []map[string]any {
		var records []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			require.NoError(t, json.Unmarshal([]byte(line), &rec))
			records = append(records, rec)
		}
		return records
	}
}

// withRequestContext adds the request ID and client IP the security event
// must carry.
func withRequestContext(r *http.Request) *http.Request {
	ctx := reqctx.WithRequestID(r.Context(), "req-123")
	ctx = reqctx.WithRequestInfo(ctx, reqctx.RequestInfo{IP: "203.0.113.7", UserAgent: "test"})
	return r.WithContext(ctx)
}

func TestValidator_WriteErrors_LogsSecurityEvent(t *testing.T) {
	l, records := captureLogger(t)
	d := request.New(request.WithLogger(l))
	r := withRequestContext(httptest.NewRequest(http.MethodGet, "/items?title=s3cr3t-value", nil))
	w := httptest.NewRecorder()

	v := d.NewValidator()
	v.AddError("title", "title must be at most 3 characters long")
	v.AddError("kind", "kind is not allowed")
	v.WriteErrors(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	recs := records()
	require.Len(t, recs, 1)
	rec := recs[0]
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, "request validation failed", rec["msg"])
	assert.EqualValues(t, http.StatusBadRequest, rec["status"])
	assert.Equal(t, "VALIDATION_ERROR", rec["code"])
	assert.Equal(t, []any{"kind", "title"}, rec["fields"], "field names, sorted")
	assert.Equal(t, http.MethodGet, rec["method"])
	assert.Equal(t, "/items", rec["path"])
	assert.Equal(t, "203.0.113.7", rec["ip"])
	assert.Equal(t, "req-123", rec["request_id"])

	line, err := json.Marshal(rec)
	require.NoError(t, err)
	assert.NotContains(t, string(line), "s3cr3t-value", "the query string is never logged")
	assert.NotContains(t, string(line), "at most 3 characters", "error messages are never logged")
}

func TestDecoder_JSON_FailureLogsSecurityEventWithoutPayload(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		limit       int64
		wantStatus  int
		wantCode    string
	}{
		{name: "malformed JSON", contentType: "application/json", body: `{"token":"s3cr3t-value"`, limit: 1 << 20, wantStatus: http.StatusBadRequest, wantCode: "INVALID_JSON"},
		{name: "unknown field", contentType: "application/json", body: `{"s3cr3t-value":1}`, limit: 1 << 20, wantStatus: http.StatusBadRequest, wantCode: "UNKNOWN_FIELD"},
		{name: "wrong type", contentType: "application/json", body: `{"name":["s3cr3t-value"]}`, limit: 1 << 20, wantStatus: http.StatusBadRequest, wantCode: "INVALID_JSON"},
		{name: "empty body", contentType: "application/json", body: ``, limit: 1 << 20, wantStatus: http.StatusBadRequest, wantCode: "INVALID_JSON"},
		{name: "trailing data", contentType: "application/json", body: `{"name":"a"} "s3cr3t-value"`, limit: 1 << 20, wantStatus: http.StatusBadRequest, wantCode: "INVALID_JSON"},
		{name: "body too large", contentType: "application/json", body: `{"name":"s3cr3t-value"}`, limit: 8, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "BODY_TOO_LARGE"},
		{name: "unsupported media type", contentType: "text/plain", body: `s3cr3t-value`, limit: 1 << 20, wantStatus: http.StatusUnsupportedMediaType, wantCode: "UNSUPPORTED_MEDIA_TYPE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, records := captureLogger(t)
			d := request.New(request.WithLogger(l))
			r := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", tt.contentType)
			r = withRequestContext(r)
			w := httptest.NewRecorder()

			var dst struct {
				Name string `json:"name"`
			}
			err := d.JSONWithLimit(w, r, &dst, tt.limit)

			require.Error(t, err)
			assert.Equal(t, tt.wantStatus, w.Code)
			recs := records()
			require.Len(t, recs, 1)
			rec := recs[0]
			assert.Equal(t, "WARN", rec["level"])
			assert.Equal(t, "request validation failed", rec["msg"])
			assert.EqualValues(t, tt.wantStatus, rec["status"])
			assert.Equal(t, tt.wantCode, rec["code"])
			assert.Equal(t, "/items", rec["path"])
			assert.Equal(t, "203.0.113.7", rec["ip"])
			assert.Equal(t, "req-123", rec["request_id"])
			assert.NotContains(t, rec, "fields")

			line, err := json.Marshal(rec)
			require.NoError(t, err)
			assert.NotContains(t, string(line), "s3cr3t-value", "the body is never logged")
		})
	}
}

func TestDecoder_JSON_SuccessLogsNothing(t *testing.T) {
	l, records := captureLogger(t)
	d := request.New(request.WithLogger(l))
	w := httptest.NewRecorder()

	var dst struct {
		Name string `json:"name"`
	}
	require.NoError(t, d.JSON(w, jsonRequest(`{"name":"ok"}`), &dst))

	v := d.NewValidator()
	v.MaxLen("name", dst.Name, 10)
	assert.False(t, v.HasErrors())
	assert.Empty(t, records())
}

// Without WithLogger, the event goes to slog.Default() as it is when the
// failure happens, so a logger installed with slog.SetDefault after the
// Decoder was built still receives it. Not parallel: it swaps the default.
func TestValidator_WriteErrors_DefaultsToSlogDefault(t *testing.T) {
	l, records := captureLogger(t)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(l)

	v := request.NewValidator()
	v.AddError("title", "required")
	v.WriteErrors(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/items", nil))

	recs := records()
	require.Len(t, recs, 1)
	assert.Equal(t, "request validation failed", recs[0]["msg"])
	assert.Equal(t, []any{"title"}, recs[0]["fields"])
}

func TestLogRejection_LogsSecurityEventWithoutFields(t *testing.T) {
	l, records := captureLogger(t)
	r := withRequestContext(httptest.NewRequest(http.MethodPost, "/items?token=s3cr3t-value", nil))

	request.LogRejection(l, r, http.StatusBadRequest, "INVALID_JSON")

	recs := records()
	require.Len(t, recs, 1)
	rec := recs[0]
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, "request validation failed", rec["msg"])
	assert.EqualValues(t, http.StatusBadRequest, rec["status"])
	assert.Equal(t, "INVALID_JSON", rec["code"])
	assert.Equal(t, http.MethodPost, rec["method"])
	assert.Equal(t, "/items", rec["path"])
	assert.Equal(t, "203.0.113.7", rec["ip"])
	assert.Equal(t, "req-123", rec["request_id"])
	assert.NotContains(t, rec, "fields")
}
