package auth

import (
	"context"
	"testing"
)

func TestPrincipal_HasRole(t *testing.T) {
	p := &Principal{Roles: []string{"admin", "editor"}}

	if !p.HasRole("admin") {
		t.Error("expected HasRole(\"admin\") to be true")
	}
	if p.HasRole("viewer") {
		t.Error("expected HasRole(\"viewer\") to be false")
	}
}

func TestPrincipal_HasRole_NilPrincipal(t *testing.T) {
	var p *Principal
	if p.HasRole("admin") {
		t.Error("expected HasRole on a nil Principal to be false, not panic")
	}
}

func TestWithPrincipal_FromContext_RoundTrips(t *testing.T) {
	want := &Principal{UserID: "u1", Username: "alice", Roles: []string{"admin"}}
	ctx := WithPrincipal(context.Background(), want)

	got := FromContext(ctx)
	if got != want {
		t.Errorf("FromContext = %+v, want %+v", got, want)
	}
}

func TestFromContext_NoPrincipal_ReturnsNil(t *testing.T) {
	if got := FromContext(context.Background()); got != nil {
		t.Errorf("FromContext on empty context = %+v, want nil", got)
	}
}
