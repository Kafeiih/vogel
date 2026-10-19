package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRecovery_NoPanic_PassesThrough(t *testing.T) {
	logger := slog.Default()
	handler := Recovery(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestRecovery_Panic_Returns500(t *testing.T) {
	logger := slog.Default()
	handler := Recovery(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("something went wrong")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	// Should not panic the test
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != "INTERNAL_ERROR" {
		t.Errorf("code = %v, want %q", body["code"], "INTERNAL_ERROR")
	}
}

func TestRecovery_PanicWithError_Returns500(t *testing.T) {
	logger := slog.Default()
	handler := Recovery(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(42) // panic with non-string value
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// recoveryLogLine panics with v behind Recovery and returns the decoded log
// line.
func recoveryLogLine(t *testing.T, v any) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	h := Recovery(slog.New(slog.NewJSONHandler(&buf, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(v)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("decode log line %q: %v", buf.String(), err)
	}
	return line
}

func TestRecovery_LogsPanicTypeAndShortValue(t *testing.T) {
	line := recoveryLogLine(t, errors.New("nil map write"))

	if line["error"] != "nil map write" {
		t.Errorf("error = %v, want the panic value", line["error"])
	}
	if line["panic_type"] != "*errors.errorString" {
		t.Errorf("panic_type = %v, want *errors.errorString", line["panic_type"])
	}
	if _, ok := line["error_truncated"]; ok {
		t.Errorf("error_truncated set for a short value")
	}
	if s, _ := line["stack"].(string); !strings.Contains(s, "goroutine") {
		t.Errorf("stack missing: %v", line["stack"])
	}
}

func TestRecovery_TruncatesLongPanicValue(t *testing.T) {
	v := strings.Repeat("x", maxLoggedPanicBytes) + "secret-tail"
	line := recoveryLogLine(t, v)

	got, _ := line["error"].(string)
	if len(got) > maxLoggedPanicBytes || strings.Contains(got, "secret-tail") {
		t.Errorf("error not bounded: len %d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncated value is not valid UTF-8")
	}
	if line["error_truncated"] != true {
		t.Errorf("error_truncated = %v, want true", line["error_truncated"])
	}
	if line["panic_type"] != "string" {
		t.Errorf("panic_type = %v, want string", line["panic_type"])
	}
}

func TestRecovery_TruncatesOnARuneBoundary(t *testing.T) {
	v := strings.Repeat("x", maxLoggedPanicBytes-1) + "é" + "tail"
	line := recoveryLogLine(t, v)

	got, _ := line["error"].(string)
	if got != strings.Repeat("x", maxLoggedPanicBytes-1) {
		t.Errorf("error = %d bytes, want the %d bytes before the split rune", len(got), maxLoggedPanicBytes-1)
	}
	if line["error_truncated"] != true {
		t.Errorf("error_truncated = %v, want true", line["error_truncated"])
	}
}

type panicPayload struct {
	Route  string
	secret string
}

// A struct panic value is logged by type only: fmt would print its
// unexported fields, which can hold request data or credentials.
func TestRecovery_StructPanicLogsOnlyItsType(t *testing.T) {
	line := recoveryLogLine(t, panicPayload{Route: "/x", secret: "hunter2"})

	if v, ok := line["error"]; ok {
		t.Errorf("error = %v, want it absent for a struct value", v)
	}
	if line["panic_type"] != "middleware.panicPayload" {
		t.Errorf("panic_type = %v, want middleware.panicPayload", line["panic_type"])
	}
}

func TestRecovery_NumberPanicLogsItsValue(t *testing.T) {
	line := recoveryLogLine(t, 42)
	if line["error"] != "42" || line["panic_type"] != "int" {
		t.Errorf("(error, panic_type) = (%v, %v), want (42, int)", line["error"], line["panic_type"])
	}
}

type nilErr struct{ msg string }

func (e *nilErr) Error() string { return e.msg }

// A typed-nil error whose Error method dereferences its receiver must not
// crash the recovery path.
func TestRecovery_TypedNilErrorDoesNotCrash(t *testing.T) {
	var e *nilErr
	line := recoveryLogLine(t, error(e))
	if line["panic_type"] != "*middleware.nilErr" {
		t.Errorf("panic_type = %v, want *middleware.nilErr", line["panic_type"])
	}
	// fmt recovers the nil dereference and prints a nil receiver as <nil>.
	if line["error"] != "<nil>" {
		t.Errorf("error = %v, want <nil>", line["error"])
	}
}

type sentinel string

// A named scalar type that is not an error keeps its value: it has no fields
// to leak.
func TestRecovery_NamedScalarPanicLogsItsValue(t *testing.T) {
	line := recoveryLogLine(t, sentinel("queue closed"))
	if line["error"] != "queue closed" || line["panic_type"] != "middleware.sentinel" {
		t.Errorf("(error, panic_type) = (%v, %v), want (queue closed, middleware.sentinel)", line["error"], line["panic_type"])
	}
}

type loudSentinel string

func (loudSentinel) String() string { return "token=hunter2" }

// A named scalar is logged from its underlying value, not through its String
// method, which could return anything.
func TestRecovery_NamedScalarIgnoresItsStringMethod(t *testing.T) {
	line := recoveryLogLine(t, loudSentinel("queue closed"))
	if line["error"] != "queue closed" {
		t.Errorf("error = %v, want the underlying value", line["error"])
	}
}
