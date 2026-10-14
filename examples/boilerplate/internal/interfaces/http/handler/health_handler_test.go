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
	"time"
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

// While the database stays down, a different failure cause (the network is
// back but the password was rotated, say) is new information and is logged
// once; repeating the same cause is not.
func TestHealth_LogsWhenTheFailureCauseChanges(t *testing.T) {
	pinger := &fakePinger{err: errors.New("connection refused")}
	rec := &recordingHandler{}
	h := NewHealthHandler(pinger, slog.New(rec))

	probe := func() {
		t.Helper()
		w := httptest.NewRecorder()
		h.Ready(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET /readyz = %d, want %d", w.Code, http.StatusServiceUnavailable)
		}
	}

	probe()
	probe()
	if got := rec.take(); len(got) != 1 || got[0] != slog.LevelError {
		t.Fatalf("first cause: logged levels %v, want [ERROR]", got)
	}

	pinger.err = errors.New("password authentication failed")
	probe()
	probe()
	if got := rec.take(); len(got) != 1 || got[0] != slog.LevelError {
		t.Fatalf("changed cause: logged levels %v, want [ERROR]", got)
	}

	pinger.err = nil
	w := httptest.NewRecorder()
	h.Ready(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if got := rec.take(); len(got) != 1 || got[0] != slog.LevelInfo {
		t.Fatalf("recovery: logged levels %v, want [INFO]", got)
	}
}

// ctxPinger fails with the context's error once it is done, like pgx does
// when the caller goes away mid-ping.
type ctxPinger struct{ err error }

func (p *ctxPinger) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.err
}

// A ping cut short because the caller hung up says nothing about the
// database: it is neither logged nor recorded as a state change, so it
// cannot open (or close) an outage in the log.
func TestHealth_CallerCancellationIsNotADatabaseFailure(t *testing.T) {
	for _, tt := range []struct {
		name  string
		probe func(*HealthHandler) http.HandlerFunc
	}{
		{"healthz", func(h *HealthHandler) http.HandlerFunc { return h.Check }},
		{"readyz", func(h *HealthHandler) http.HandlerFunc { return h.Ready }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pinger := &ctxPinger{}
			rec := &recordingHandler{}
			h := NewHealthHandler(pinger, slog.New(rec))

			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			req := httptest.NewRequest(http.MethodGet, "/"+tt.name, nil).WithContext(canceled)
			w := httptest.NewRecorder()
			tt.probe(h)(w, req)

			if got := rec.take(); len(got) != 0 {
				t.Fatalf("canceled caller: logged levels %v, want none", got)
			}
			if w.Code == http.StatusServiceUnavailable {
				t.Errorf("canceled caller answered 503: the database was reported down")
			}

			// The database was never marked down, so a healthy probe now has
			// no recovery to log...
			w = httptest.NewRecorder()
			tt.probe(h)(w, httptest.NewRequest(http.MethodGet, "/"+tt.name, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("healthy probe = %d, want %d", w.Code, http.StatusOK)
			}
			if got := rec.take(); len(got) != 0 {
				t.Fatalf("healthy probe after a canceled one: logged levels %v, want none", got)
			}

			// ...and during a real outage a canceled probe does not end it.
			pinger.err = errors.New("connection refused")
			tt.probe(h)(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/"+tt.name, nil))
			rec.take()
			tt.probe(h)(httptest.NewRecorder(), req)
			tt.probe(h)(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/"+tt.name, nil))
			if got := rec.take(); len(got) != 0 {
				t.Fatalf("outage with a canceled probe in between: logged levels %v, want none", got)
			}
		})
	}
}

// hangingPinger never answers on its own; it returns only when ctx is done,
// like a database that accepts the connection and then stalls.
type hangingPinger struct{}

func (hangingPinger) Ping(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// A caller deadline (a kubelet probe timeout, an upstream timeout middleware)
// expiring while the database stalls is the outage itself, not a client that
// went away: it is logged and answered 503, so a hung database is never
// hidden behind the caller's shorter deadline.
func TestHealth_CallerDeadlineOnAHungDatabaseIsAFailure(t *testing.T) {
	for _, tt := range []struct {
		name  string
		probe func(*HealthHandler) http.HandlerFunc
	}{
		{"healthz", func(h *HealthHandler) http.HandlerFunc { return h.Check }},
		{"readyz", func(h *HealthHandler) http.HandlerFunc { return h.Ready }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recordingHandler{}
			h := NewHealthHandler(hangingPinger{}, slog.New(rec))

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			req := httptest.NewRequest(http.MethodGet, "/"+tt.name, nil).WithContext(ctx)
			w := httptest.NewRecorder()
			tt.probe(h)(w, req)

			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("hung database under a caller deadline = %d, want %d", w.Code, http.StatusServiceUnavailable)
			}
			if got := rec.take(); len(got) != 1 || got[0] != slog.LevelError {
				t.Errorf("hung database under a caller deadline: logged levels %v, want [ERROR]", got)
			}
		})
	}
}
