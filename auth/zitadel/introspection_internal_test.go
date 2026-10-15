package zitadel

import (
	"container/list"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zitadel/zitadel-go/v3/pkg/authorization/oauth"
)

// TestSharedResult covers what a singleflight result can hold: a valid
// result is copied, anything else is an introspection failure, never a
// panic.
func TestSharedResult(t *testing.T) {
	t.Run("copies a valid result", func(t *testing.T) {
		orig := &oauth.IntrospectionContext{}
		orig.Active = true
		got, err := sharedResult(orig)
		if err != nil {
			t.Fatalf("sharedResult(valid) err = %v, want nil", err)
		}
		if got == orig {
			t.Fatal("sharedResult returned the shared pointer, want a copy")
		}
		if !got.Active {
			t.Fatal("sharedResult copy lost Active")
		}
	})

	for name, v := range map[string]any{
		"typed nil":   (*oauth.IntrospectionContext)(nil),
		"untyped nil": nil,
		"wrong type":  "not a result",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := sharedResult(v)
			if !errors.Is(err, oauth.ErrIntrospectionFailed) {
				t.Fatalf("sharedResult(%s) err = %v, want ErrIntrospectionFailed", name, err)
			}
			if got != nil {
				t.Fatalf("sharedResult(%s) = %v, want nil", name, got)
			}
		})
	}
}

// TestEntryOf_PanicsOnForeignValue pins the invariant entryOf guards: the
// LRU list only holds *cacheEntry, and anything else panics with the
// offending type instead of a bare type-assertion panic.
func TestEntryOf_PanicsOnForeignValue(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("entryOf(foreign value) did not panic")
		}
		msg := fmt.Sprint(r)
		if !strings.Contains(msg, "string") || !strings.Contains(msg, "*cacheEntry") {
			t.Fatalf("panic = %q, want it to name the held type and *cacheEntry", msg)
		}
	}()
	entryOf(list.New().PushBack("foreign"))
}
