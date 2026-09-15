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

	"github.com/kafeiih/vogel/access"
	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
)

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
