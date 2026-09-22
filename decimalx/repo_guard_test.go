package decimalx_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kafeiih/vogel/decimalx/decimalxtest"
)

// TestNoDirectNewFromStringInRepo is the structural gate: it FAILS if
// decimal.NewFromString, decimal.RequireFromString, or
// decimal.NewFromFormattedString is called anywhere in this module outside
// decimalx itself. Every decimal string coming from outside the process must
// go through decimalx.Parse, which bounds the coefficient and exponent so a
// worker does not hang materializing the digits (see decimalx's package
// doc). A new call site using any of these three constructors directly
// would reopen that DoS hole; this test blocks it.
//
// It is a source-text scan, not a type-aware one: it cannot catch an aliased
// shopspring/decimal import, and it has no call site to catch at all when
// encoding/json decodes a payload directly into a decimal.Decimal field —
// that path relies on decimalx.ValidateBounds/ValidateAmount being run on
// the decoded value instead (see AssertNoDirectNewFromString's doc for the
// full list of what this guard cannot detect).
func TestNoDirectNewFromStringInRepo(t *testing.T) {
	root := repoRoot(t)
	decimalxtest.AssertNoDirectNewFromString(t, root, ".")
}

// repoRoot walks up from the test's working directory until it finds
// go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod walking up from the test's working directory")
		}
		dir = parent
	}
}
