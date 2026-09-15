package access

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
)

// recordingChecker implements authz.Checker with a canned result, recording
// every call it receives so tests can assert on call count and on the exact
// Principal/Resource the Guard forwarded.
type recordingChecker struct {
	allowed bool
	err     error

	mu    sync.Mutex
	calls int
	gotP  authz.Principal
	gotR  authz.Resource
}

func (c *recordingChecker) IsAllowed(_ context.Context, p authz.Principal, r authz.Resource, _ string) (bool, error) {
	c.mu.Lock()
	c.calls++
	c.gotP = p
	c.gotR = r
	c.mu.Unlock()
	return c.allowed, c.err
}

func (c *recordingChecker) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func ctxWithPrincipal(p *auth.Principal) context.Context {
	return auth.WithPrincipal(context.Background(), p)
}

func TestGuardCheck_NoPrincipal_ReturnsErrUnauthenticatedAndSkipsChecker(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	g := New(checker)

	err := g.Check(context.Background(), authz.Resource{Kind: "docs", ID: "1"}, "read")

	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if checker.callCount() != 0 {
		t.Fatalf("checker called %d times, want 0", checker.callCount())
	}
}

func TestGuardCheck_CheckerDenies_ReturnsErrForbidden(t *testing.T) {
	checker := &recordingChecker{allowed: false}
	g := New(checker)
	ctx := ctxWithPrincipal(&auth.Principal{UserID: "u1"})

	err := g.Check(ctx, authz.Resource{Kind: "docs", ID: "1"}, "read")

	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestGuardCheck_CheckerErrors_ReturnsErrUnavailableWrappingCause(t *testing.T) {
	cause := errors.New("pdp: connection refused")
	checker := &recordingChecker{err: cause}
	g := New(checker)
	ctx := ctxWithPrincipal(&auth.Principal{UserID: "u1"})

	err := g.Check(ctx, authz.Resource{Kind: "docs", ID: "1"}, "read")

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want it to wrap cause %v", err, cause)
	}
}

func TestGuardCheck_ResolverErrors_ReturnsErrUnavailableAndSkipsChecker(t *testing.T) {
	cause := errors.New("assignment service down")
	checker := &recordingChecker{allowed: true}
	g := New(checker, WithPrincipalAttributes(func(context.Context, *auth.Principal) (map[string]any, error) {
		return nil, cause
	}))
	ctx := ctxWithPrincipal(&auth.Principal{UserID: "u1"})

	err := g.Check(ctx, authz.Resource{Kind: "docs", ID: "1"}, "read")

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want it to wrap cause %v", err, cause)
	}
	if checker.callCount() != 0 {
		t.Fatalf("checker called %d times, want 0 (resolver failed before the checker could run)", checker.callCount())
	}
}

func TestGuardCheck_ResourceAttrReachesChecker_Intact(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	g := New(checker)
	ctx := ctxWithPrincipal(&auth.Principal{UserID: "u1"})

	resource := authz.Resource{Kind: "docs", ID: "1", Attr: map[string]any{"owner": "u1", "status": "draft"}}
	if err := g.Check(ctx, resource, "read"); err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	if !reflect.DeepEqual(checker.gotR, resource) {
		t.Errorf("checker got resource %+v, want %+v", checker.gotR, resource)
	}
}

func TestGuardCheck_ResolverResult_BecomesPrincipalAttr(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	want := map[string]any{"owned_ids": []string{"1", "2"}}
	g := New(checker, WithPrincipalAttributes(func(context.Context, *auth.Principal) (map[string]any, error) {
		return want, nil
	}))
	ctx := ctxWithPrincipal(&auth.Principal{UserID: "u1", Roles: []string{"editor"}})

	if err := g.Check(ctx, authz.Resource{Kind: "docs", ID: "1"}, "read"); err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	wantPrincipal := authz.Principal{ID: "u1", Roles: []string{"editor"}, Attr: want}
	if !reflect.DeepEqual(checker.gotP, wantPrincipal) {
		t.Errorf("checker got principal %+v, want %+v", checker.gotP, wantPrincipal)
	}
}

func TestGuardCheck_NoResolverConfigured_AttrStaysNil(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	g := New(checker)
	ctx := ctxWithPrincipal(&auth.Principal{UserID: "u1"})

	if err := g.Check(ctx, authz.Resource{Kind: "docs", ID: "1"}, "read"); err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	if checker.gotP.Attr != nil {
		t.Errorf("principal.Attr = %#v, want nil", checker.gotP.Attr)
	}
}

func TestGuardCheck_WithRequestScope_ResolverCalledOnce(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	var resolverCalls int32
	g := New(checker, WithPrincipalAttributes(func(context.Context, *auth.Principal) (map[string]any, error) {
		atomic.AddInt32(&resolverCalls, 1)
		return map[string]any{"k": "v"}, nil
	}))

	p := &auth.Principal{UserID: "u1"}
	ctx := WithRequestScope(ctxWithPrincipal(p))

	if err := g.Check(ctx, authz.Resource{Kind: "docs", ID: "1"}, "read"); err != nil {
		t.Fatalf("Check() #1 error = %v", err)
	}
	if err := g.Check(ctx, authz.Resource{Kind: "docs", ID: "2"}, "read"); err != nil {
		t.Fatalf("Check() #2 error = %v", err)
	}

	if got := atomic.LoadInt32(&resolverCalls); got != 1 {
		t.Errorf("resolver called %d times, want 1", got)
	}
}

func TestGuardCheck_WithoutRequestScope_ResolverCalledEveryTime(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	var resolverCalls int32
	g := New(checker, WithPrincipalAttributes(func(context.Context, *auth.Principal) (map[string]any, error) {
		atomic.AddInt32(&resolverCalls, 1)
		return map[string]any{"k": "v"}, nil
	}))

	ctx := ctxWithPrincipal(&auth.Principal{UserID: "u1"})

	if err := g.Check(ctx, authz.Resource{Kind: "docs", ID: "1"}, "read"); err != nil {
		t.Fatalf("Check() #1 error = %v", err)
	}
	if err := g.Check(ctx, authz.Resource{Kind: "docs", ID: "2"}, "read"); err != nil {
		t.Fatalf("Check() #2 error = %v", err)
	}

	if got := atomic.LoadInt32(&resolverCalls); got != 2 {
		t.Errorf("resolver called %d times, want 2 (no scope in ctx)", got)
	}
}

func TestGuardCheck_WithRequestScope_ResolverErrorIsMemoizedToo(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	cause := errors.New("assignment service down")
	var resolverCalls int32
	g := New(checker, WithPrincipalAttributes(func(context.Context, *auth.Principal) (map[string]any, error) {
		atomic.AddInt32(&resolverCalls, 1)
		return nil, cause
	}))

	ctx := WithRequestScope(ctxWithPrincipal(&auth.Principal{UserID: "u1"}))

	err1 := g.Check(ctx, authz.Resource{Kind: "docs", ID: "1"}, "read")
	err2 := g.Check(ctx, authz.Resource{Kind: "docs", ID: "2"}, "read")

	if !errors.Is(err1, ErrUnavailable) || !errors.Is(err2, ErrUnavailable) {
		t.Fatalf("err1 = %v, err2 = %v, want both to wrap ErrUnavailable", err1, err2)
	}
	if got := atomic.LoadInt32(&resolverCalls); got != 1 {
		t.Errorf("resolver called %d times, want 1 (error must be memoized too)", got)
	}
}

func TestGuardCheck_WithRequestScope_ConcurrentCallsResolveOnce(t *testing.T) {
	checker := &recordingChecker{allowed: true}
	var resolverCalls int32
	g := New(checker, WithPrincipalAttributes(func(context.Context, *auth.Principal) (map[string]any, error) {
		atomic.AddInt32(&resolverCalls, 1)
		return map[string]any{"k": "v"}, nil
	}))

	ctx := WithRequestScope(ctxWithPrincipal(&auth.Principal{UserID: "u1"}))

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]error, n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			errs[i] = g.Check(ctx, authz.Resource{Kind: "docs", ID: "1"}, "read")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: Check() error = %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&resolverCalls); got != 1 {
		t.Errorf("resolver called %d times, want 1", got)
	}
}

func TestNew_NilChecker_Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New(nil) did not panic")
		}
	}()
	New(nil)
}
