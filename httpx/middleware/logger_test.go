package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kafeiih/vogel/reqctx"
)

// logLine runs h against one request and returns the single structured log
// record StructuredLogger emitted for it.
func logLine(t *testing.T, r *http.Request) map[string]any {
	t.Helper()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	h := StructuredLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), r)

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("log line is not valid JSON: %v (%q)", err, buf.String())
	}
	return got
}

// RequestContext's doc comment promises a log line and an audit_log row for
// the same request are provably linked, because both read the request ID from
// reqctx. StructuredLogger used to read chi's GetReqID instead, so the access
// log only agreed with audit by coincidence — both happened to derive from the
// same upstream value. Any context carrying a request ID that did not come
// from chi (a worker deriving one from the request that spawned it, a test)
// logged an empty request_id while audit recorded the real one.
func TestStructuredLogger_ReadsRequestIDFromReqctx(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/things", nil)
	r = r.WithContext(reqctx.WithRequestID(r.Context(), "req-abc-123"))

	got := logLine(t, r)

	if got["request_id"] != "req-abc-123" {
		t.Errorf("request_id = %v, want %q", got["request_id"], "req-abc-123")
	}
}

// With no request ID anywhere the field is present and empty rather than
// missing, so a log pipeline sees a consistent shape.
func TestStructuredLogger_NoRequestID_LogsEmpty(t *testing.T) {
	got := logLine(t, httptest.NewRequest(http.MethodGet, "/things", nil))

	if got["request_id"] != "" {
		t.Errorf("request_id = %v, want empty", got["request_id"])
	}
}
