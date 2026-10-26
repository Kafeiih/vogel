package validation_test

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
	"github.com/kafeiih/vogel/validation"
)

type logTestPayload struct {
	Name  string `json:"name" validate:"required"`
	Email string `json:"email" validate:"required,email"`
}

func decodeRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
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

// A struct validation failure is an input rejection like request's: it logs
// the same security event, with the failing JSON field names and never their
// values.
func TestWrite_InvalidLogsSecurityEvent(t *testing.T) {
	var buf bytes.Buffer
	v := validation.New(validation.WithLogger(slog.New(slog.NewJSONHandler(&buf, nil))))
	r := httptest.NewRequest(http.MethodPost, "/items", nil)
	r = r.WithContext(reqctx.WithRequestID(r.Context(), "req-9"))

	ok := v.Write(httptest.NewRecorder(), r, &logTestPayload{Email: "s3cr3t-not-an-email"})

	assert.False(t, ok)
	recs := decodeRecords(t, &buf)
	require.Len(t, recs, 1)
	rec := recs[0]
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, "request validation failed", rec["msg"])
	assert.EqualValues(t, http.StatusBadRequest, rec["status"])
	assert.Equal(t, "VALIDATION_ERROR", rec["code"])
	assert.Equal(t, []any{"email", "name"}, rec["fields"])
	assert.Equal(t, "/items", rec["path"])
	assert.Equal(t, "req-9", rec["request_id"])
	assert.NotContains(t, buf.String(), "s3cr3t-not-an-email")
}

func TestWrite_ValidLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	v := validation.New(validation.WithLogger(slog.New(slog.NewJSONHandler(&buf, nil))))

	ok := v.Write(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil),
		&logTestPayload{Name: "a", Email: "a@example.com"})

	assert.True(t, ok)
	assert.Empty(t, buf.String())
}
