package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakePinger struct {
	err error
}

func (f *fakePinger) Ping(context.Context) error {
	return f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- /livez ---

func TestLive_Always200(t *testing.T) {
	h := NewHealthHandler(&fakePinger{}, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	rec := httptest.NewRecorder()
	h.Live(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	data := body["data"].(map[string]any)
	if data["status"] != "alive" {
		t.Errorf("status = %v, want %q", data["status"], "alive")
	}
}

// --- /healthz ---

func TestCheck_Healthy_Returns200(t *testing.T) {
	h := NewHealthHandler(&fakePinger{err: nil}, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.Check(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	data := body["data"].(map[string]any)
	if data["status"] != "ok" {
		t.Errorf("status = %v, want %q", data["status"], "ok")
	}
	checks := data["checks"].(map[string]any)
	db := checks["database"].(map[string]any)
	if db["status"] != "up" {
		t.Errorf("db status = %v, want %q", db["status"], "up")
	}
}

func TestCheck_DBDown_Returns503(t *testing.T) {
	h := NewHealthHandler(&fakePinger{err: errors.New("connection refused")}, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.Check(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	data := body["data"].(map[string]any)
	if data["status"] != "degraded" {
		t.Errorf("status = %v, want %q", data["status"], "degraded")
	}
	checks := data["checks"].(map[string]any)
	db := checks["database"].(map[string]any)
	if db["status"] != "down" {
		t.Errorf("db status = %v, want %q", db["status"], "down")
	}
	if _, ok := db["error"]; ok {
		t.Errorf("database check has an error field %v; the cause must stay server-side", db["error"])
	}
}

// /healthz is public and unauthenticated: the database driver's error text
// (host names, ports, role names, TLS details) must never reach the caller.
func TestCheck_DBDown_DoesNotLeakErrorText(t *testing.T) {
	const secret = "dial tcp 10.1.2.3:5432: password authentication failed for user app_runtime"
	var logs bytes.Buffer
	h := NewHealthHandler(&fakePinger{err: errors.New(secret)}, slog.New(slog.NewTextHandler(&logs, nil)))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.Check(rec, req)

	body := rec.Body.String()
	for _, fragment := range []string{secret, "10.1.2.3", "app_runtime", "password"} {
		if strings.Contains(body, fragment) {
			t.Errorf("response body leaks %q: %s", fragment, body)
		}
	}
	// The cause is not lost: operators find it in the server log.
	if !strings.Contains(logs.String(), "password authentication failed") {
		t.Errorf("server log does not record the ping error: %q", logs.String())
	}
}

// --- /readyz ---

func TestReady_Healthy_Returns200(t *testing.T) {
	h := NewHealthHandler(&fakePinger{err: nil}, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	data := body["data"].(map[string]any)
	if data["status"] != "ready" {
		t.Errorf("status = %v, want %q", data["status"], "ready")
	}
}

func TestReady_DBDown_Returns503(t *testing.T) {
	h := NewHealthHandler(&fakePinger{err: errors.New("timeout")}, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	data := body["data"].(map[string]any)
	if data["status"] != "not_ready" {
		t.Errorf("status = %v, want %q", data["status"], "not_ready")
	}
}

// --- logging on state transitions ---

// recordingHandler is a slog.Handler that keeps every record it receives.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// take returns the levels recorded since the previous call and resets them.
func (h *recordingHandler) take() []slog.Level {
	h.mu.Lock()
	defer h.mu.Unlock()
	levels := make([]slog.Level, 0, len(h.records))
	for _, r := range h.records {
		levels = append(levels, r.Level)
	}
	h.records = nil
	return levels
}

// The probes are public and unauthenticated: logging at ERROR on every
// failed ping would let anyone flood the log while the database is down.
// The handler logs only when the database state changes, and the responses
// stay exactly as before.
func TestHealth_LogsOnlyOnStateTransitions(t *testing.T) {
	pinger := &fakePinger{}
	rec := &recordingHandler{}
	h := NewHealthHandler(pinger, slog.New(rec))

	probe := func(handler http.HandlerFunc, path string, wantCode int) {
		t.Helper()
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != wantCode {
			t.Fatalf("GET %s = %d, want %d", path, w.Code, wantCode)
		}
	}
	expect := func(step string, want ...slog.Level) {
		t.Helper()
		got := rec.take()
		if len(got) != len(want) {
			t.Fatalf("%s: logged levels %v, want %v", step, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: logged levels %v, want %v", step, got, want)
			}
		}
	}

	probe(h.Check, "/healthz", http.StatusOK)
	probe(h.Ready, "/readyz", http.StatusOK)
	expect("healthy from the start")

	pinger.err = errors.New("connection refused")
	probe(h.Check, "/healthz", http.StatusServiceUnavailable)
	expect("first failure", slog.LevelError)

	for range 5 {
		probe(h.Check, "/healthz", http.StatusServiceUnavailable)
		probe(h.Ready, "/readyz", http.StatusServiceUnavailable)
	}
	expect("repeated failures")

	pinger.err = nil
	probe(h.Ready, "/readyz", http.StatusOK)
	expect("recovery", slog.LevelInfo)

	probe(h.Check, "/healthz", http.StatusOK)
	expect("still healthy")

	pinger.err = errors.New("timeout")
	probe(h.Ready, "/readyz", http.StatusServiceUnavailable)
	expect("second outage", slog.LevelError)
}
