package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kafeiih/vogel/access"
	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
	"github.com/kafeiih/vogel/reqctx"
)

// capturingHandler is a slog.Handler that keeps every record it receives so
// a test can assert on the level and attributes of what a middleware logged,
// without parsing text or JSON output.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *capturingHandler) WithGroup(string) slog.Handler { return h }

func (h *capturingHandler) all() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

// attrs flattens a record's attributes into a map keyed by attribute name.
func attrs(r slog.Record) map[string]any {
	out := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value.Any()
		return true
	})
	return out
}

// withRouteParam attaches a chi route context carrying one URL parameter to
// r, so chi.URLParam resolves it exactly as it would inside a mounted router.
func withRouteParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// recordingChecker implements authz.Checker, recording every call it
// receives (the Principal and Resource it saw, and a running call count) so
// RequireAccess and the per-instance access.Guard.Check path can each be
// asserted on without a real PDP. It is defined separately from
// authz_test.go's fakeChecker because that file must not be touched.
type recordingChecker struct {
	allowed bool
	err     error

	mu    sync.Mutex
	calls int32
	gotP  authz.Principal
	gotR  authz.Resource
}

func (c *recordingChecker) IsAllowed(_ context.Context, p authz.Principal, r authz.Resource, _ string) (bool, error) {
	atomic.AddInt32(&c.calls, 1)
	c.mu.Lock()
	c.gotP = p
	c.gotR = r
	c.mu.Unlock()
	return c.allowed, c.err
}

func (c *recordingChecker) callCount() int32 {
	return atomic.LoadInt32(&c.calls)
}

func (c *recordingChecker) lastPrincipal() authz.Principal {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gotP
}

func TestRequireAccess_WithPrincipalAttributes_SendsPrincipalAttr(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	wantAttr := map[string]any{"owned_ids": []string{"1", "2"}}
	guard := access.New(checker, access.WithPrincipalAttributes(func(context.Context, *auth.Principal) (map[string]any, error) {
		return wantAttr, nil
	}))

	called := false
	handler := RequireAccess(guard, "documents", "delete", slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := withPrincipal(httptest.NewRequest(http.MethodDelete, "/", nil), &auth.Principal{UserID: "u1"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("next.ServeHTTP was not called despite the checker allowing the request")
	}
	if !reflect.DeepEqual(checker.lastPrincipal().Attr, wantAttr) {
		t.Errorf("principal.Attr = %#v, want %#v", checker.lastPrincipal().Attr, wantAttr)
	}
}

func TestRequireAccess_PrincipalAttributesError_Returns503AndSkipsCheckerAndNext(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	guard := access.New(checker, access.WithPrincipalAttributes(func(context.Context, *auth.Principal) (map[string]any, error) {
		return nil, errors.New("assignment service down")
	}))

	called := false
	handler := RequireAccess(guard, "documents", "delete", slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := withPrincipal(httptest.NewRequest(http.MethodDelete, "/", nil), &auth.Principal{UserID: "u1"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("next.ServeHTTP was called despite a failed principal-attribute resolver (fail-open)")
	}
	if checker.callCount() != 0 {
		t.Fatalf("checker called %d times, want 0", checker.callCount())
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestRequireAccess_MiddlewareAndInstanceCheck_ResolveAttributesOnce(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	var resolverCalls int32
	wantAttr := map[string]any{"owned_ids": []string{"1"}}
	guard := access.New(checker, access.WithPrincipalAttributes(func(context.Context, *auth.Principal) (map[string]any, error) {
		atomic.AddInt32(&resolverCalls, 1)
		return wantAttr, nil
	}))

	var sawInHandler authz.Principal
	handler := RequireAccess(guard, "documents", "delete", slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate the handler's own per-instance check, made after loading
		// the entity, sharing the same request context RequireAccess passed
		// to next -- and therefore the same memoization scope.
		if err := guard.Check(r.Context(), authz.Resource{Kind: "documents:document", ID: "1", Attr: map[string]any{"owner": "u1"}}, "delete"); err != nil {
			t.Fatalf("instance Check() error = %v", err)
		}
		sawInHandler = checker.lastPrincipal()
	}))

	req := withPrincipal(httptest.NewRequest(http.MethodDelete, "/", nil), &auth.Principal{UserID: "u1"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	if got := atomic.LoadInt32(&resolverCalls); got != 1 {
		t.Errorf("resolver called %d times, want 1", got)
	}
	if checker.callCount() != 2 {
		t.Fatalf("checker called %d times, want 2 (middleware + handler)", checker.callCount())
	}
	wantPrincipal := authz.Principal{ID: "u1", Attr: wantAttr}
	if !reflect.DeepEqual(sawInHandler, wantPrincipal) {
		t.Errorf("handler-side principal = %+v, want %+v", sawInHandler, wantPrincipal)
	}
}

func TestRequireAccess_WithoutPrincipalAttributes_AttrStaysNil(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	guard := access.New(checker)

	handler := RequireAccess(guard, "documents", "read", slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), &auth.Principal{UserID: "u1"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if checker.lastPrincipal().Attr != nil {
		t.Errorf("principal.Attr = %#v, want nil", checker.lastPrincipal().Attr)
	}
}

func TestRequireAccess_Forbidden_LogsDenialWithActorResourceAndRequest(t *testing.T) {
	checker := &recordingChecker{allowed: false}
	guard := access.New(checker)
	captured := &capturingHandler{}

	called := false
	handler := RequireAccess(guard, "documents", "delete", slog.New(captured))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodDelete, "/documents/doc-42", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	req = withPrincipal(req, &auth.Principal{UserID: "u1"})
	req = withRouteParam(req, "id", "doc-42")
	req = req.WithContext(reqctx.WithRequestID(req.Context(), "req-123"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("next.ServeHTTP was called despite the checker denying the request")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	records := captured.all()
	if len(records) != 1 {
		t.Fatalf("logged %d records, want exactly 1", len(records))
	}
	r := records[0]
	if r.Level != slog.LevelWarn {
		t.Errorf("level = %v, want %v", r.Level, slog.LevelWarn)
	}
	got := attrs(r)
	want := map[string]any{
		"user_id":       "u1",
		"resource_kind": "documents",
		"resource_id":   "doc-42",
		"action":        "delete",
		"method":        http.MethodDelete,
		"path":          "/documents/doc-42",
		"request_id":    "req-123",
	}
	for key, wantVal := range want {
		if got[key] != wantVal {
			t.Errorf("attr %q = %#v, want %#v", key, got[key], wantVal)
		}
	}
	for key, val := range got {
		if s, ok := val.(string); ok && strings.Contains(s, "secret-token") {
			t.Errorf("attr %q = %q leaks the bearer token", key, s)
		}
	}
}

func TestRequireAccess_Forbidden_WithoutRouteID_LogsWildcardResourceID(t *testing.T) {
	checker := &recordingChecker{allowed: false}
	guard := access.New(checker)
	captured := &capturingHandler{}

	handler := RequireAccess(guard, "documents", "create", slog.New(captured))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := withPrincipal(httptest.NewRequest(http.MethodPost, "/documents", nil), &auth.Principal{UserID: "u1"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	records := captured.all()
	if len(records) != 1 {
		t.Fatalf("logged %d records, want exactly 1", len(records))
	}
	got := attrs(records[0])
	if got["resource_id"] != wildcardResourceID {
		t.Errorf("resource_id = %#v, want %q", got["resource_id"], wildcardResourceID)
	}
	if got["request_id"] != "" {
		t.Errorf("request_id = %#v, want empty when no request ID was bridged", got["request_id"])
	}
}

func TestRequireAccess_Unauthenticated_Returns401WithoutLogging(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	guard := access.New(checker)
	captured := &capturingHandler{}

	handler := RequireAccess(guard, "documents", "read", slog.New(captured))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	// No principal on the context: guard.Check fails with ErrUnauthenticated.
	req := httptest.NewRequest(http.MethodGet, "/documents/1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if n := len(captured.all()); n != 0 {
		t.Errorf("logged %d records on the 401 path, want 0", n)
	}
}

func TestWriteAccessError_Forbidden_LogsDenialWithoutResource(t *testing.T) {
	captured := &capturingHandler{}
	rec := httptest.NewRecorder()
	req := withPrincipal(httptest.NewRequest(http.MethodGet, "/documents/7", nil), &auth.Principal{UserID: "u2"})

	WriteAccessError(rec, req, access.ErrForbidden, slog.New(captured))

	records := captured.all()
	if len(records) != 1 {
		t.Fatalf("logged %d records, want exactly 1", len(records))
	}
	if records[0].Level != slog.LevelWarn {
		t.Errorf("level = %v, want %v", records[0].Level, slog.LevelWarn)
	}
	got := attrs(records[0])
	if got["user_id"] != "u2" {
		t.Errorf("user_id = %#v, want %q", got["user_id"], "u2")
	}
	if got["method"] != http.MethodGet || got["path"] != "/documents/7" {
		t.Errorf("method/path = %#v/%#v, want %q/%q", got["method"], got["path"], http.MethodGet, "/documents/7")
	}
	// A handler-side call knows nothing about the route, so these are empty
	// rather than fabricated.
	if got["resource_kind"] != "" || got["resource_id"] != "" || got["action"] != "" {
		t.Errorf("resource_kind/resource_id/action = %#v/%#v/%#v, want all empty", got["resource_kind"], got["resource_id"], got["action"])
	}
}

func TestWriteAccessError_Unauthenticated_Returns401(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	handled := WriteAccessError(rec, req, access.ErrUnauthenticated, slog.Default())

	if !handled {
		t.Fatal("WriteAccessError() = false, want true")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestWriteAccessError_Forbidden_Returns403(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	handled := WriteAccessError(rec, req, access.ErrForbidden, slog.Default())

	if !handled {
		t.Fatal("WriteAccessError() = false, want true")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestWriteAccessError_Unavailable_Returns503AndLogs(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	cause := errors.New("pdp down")

	handled := WriteAccessError(rec, req, errors.Join(access.ErrUnavailable, cause), slog.Default())

	if !handled {
		t.Fatal("WriteAccessError() = false, want true")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestWriteAccessError_UsesCustomAuthMessages(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	WriteAccessError(rec, req, access.ErrForbidden, slog.Default(), WithAuthMessages(AuthMessages{Forbidden: "custom forbidden message"}))

	if !strings.Contains(rec.Body.String(), "custom forbidden message") {
		t.Errorf("body = %q, want it to contain the custom message", rec.Body.String())
	}
}

func TestWriteAccessError_NonAccessError_ReturnsFalseAndWritesNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	handled := WriteAccessError(rec, req, errors.New("some domain error"), slog.Default())

	if handled {
		t.Fatal("WriteAccessError() = true, want false for an unrecognized error")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want default %d (nothing written)", rec.Code, http.StatusOK)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestWriteAccessError_Nil_ReturnsFalseAndWritesNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	handled := WriteAccessError(rec, req, nil, slog.Default())

	if handled {
		t.Fatal("WriteAccessError() = true, want false for nil")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want default %d (nothing written)", rec.Code, http.StatusOK)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestWriteAccessError_NilLogger_StillWrites403(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/documents/1", nil)

	handled := WriteAccessError(rec, req, access.ErrForbidden, nil)

	if !handled {
		t.Fatal("WriteAccessError returned false for ErrForbidden")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestWriteAccessError_Unavailable_LogsSameRequestFieldsAsDenial(t *testing.T) {
	capture := &capturingHandler{}
	logger := slog.New(capture)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/documents/1", nil)

	WriteAccessError(rec, req, access.ErrUnavailable, logger)

	records := capture.all()
	if len(records) != 1 {
		t.Fatalf("logged %d records, want exactly 1", len(records))
	}
	got := attrs(records[0])
	for _, key := range []string{"error", "user_id", "resource_kind", "resource_id", "action", "method", "path", "request_id"} {
		if _, ok := got[key]; !ok {
			t.Errorf("503 record is missing attribute %q", key)
		}
	}
	if got["method"] != http.MethodDelete || got["path"] != "/documents/1" {
		t.Errorf("method/path = %q/%q, want DELETE//documents/1", got["method"], got["path"])
	}
}
