package scaffold

import (
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	for _, n := range []string{"billing", "my-service", "a1", "a" + strings.Repeat("b", 49)} {
		if err := ValidateName(n); err != nil {
			t.Fatalf("ValidateName(%q) = %v", n, err)
		}
	}
	for _, n := range []string{"", "1abc", "-abc", "svc-", "a--b", "My-Service", "my_service", "a/b", "a" + strings.Repeat("b", 50)} {
		if err := ValidateName(n); err == nil {
			t.Fatalf("ValidateName(%q) = nil, want error", n)
		}
	}
}

func TestValidateModulePath(t *testing.T) {
	for _, m := range []string{"github.com/acme/billing", "gitlab.example.com/team/sub/billing-api", "example.com/x.v2", "github.com/Acme/Billing"} {
		if err := ValidateModulePath(m); err != nil {
			t.Fatalf("ValidateModulePath(%q) = %v", m, err)
		}
	}
	for _, m := range []string{"", "billing", "github.com", "github.com//x", "github.com/acme/../x", "/github.com/acme", "github.com/acme/", "Github.com/acme/x", "github.com/acme/my service", "-github.com/acme/x", "github.com/acme/.x"} {
		if err := ValidateModulePath(m); err == nil {
			t.Fatalf("ValidateModulePath(%q) = nil, want error", m)
		}
	}
}

func TestValidateOwner(t *testing.T) {
	for _, o := range []string{"acme", "Acme-Corp", "a1"} {
		if err := ValidateOwner(o); err != nil {
			t.Fatalf("ValidateOwner(%q) = %v", o, err)
		}
	}
	for _, o := range []string{"", "-acme", "acme-", "ac--me", "acme/x", "acme corp", strings.Repeat("a", 40)} {
		if err := ValidateOwner(o); err == nil {
			t.Fatalf("ValidateOwner(%q) = nil, want error", o)
		}
	}
}

func TestDeriveValues(t *testing.T) {
	v := Derive("billing-api", "Acme-Corp", "github.com/Acme-Corp/billing-api")
	want := TemplateValues{
		Name:      "billing-api",
		DBName:    "billing_api",
		Owner:     "Acme-Corp",
		ImageBase: "ghcr.io/acme-corp/billing-api",
		Module:    "github.com/Acme-Corp/billing-api",
	}
	if v != want {
		t.Fatalf("Derive() = %+v, want %+v", v, want)
	}
}
