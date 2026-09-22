// Package decimalxtest provides a structural test helper for consumers of
// decimalx: a guard that fails a test if any .go file outside the decimalx
// package itself calls decimal.NewFromString directly, bypassing the
// bounded parser decimalx.Parse provides.
package decimalxtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// forbiddenCall detects calls to decimal.NewFromString, tolerating spacing
// gofmt would never produce but an editor might (e.g. "decimal .NewFromString").
var forbiddenCall = regexp.MustCompile(`decimal\s*\.\s*NewFromString`)

// AssertNoDirectNewFromString fails t if decimal.NewFromString is called
// anywhere in a .go file (excluding _test.go files) under root/dir, for
// every dir in dirs, with one exception: the decimalx package's own
// directory (root/decimalx) is never scanned, since it is the only package
// allowed to call decimal.NewFromString directly.
//
// A dir that does not exist under root is silently skipped, so a caller can
// pass a fixed list of top-level directories (or "." for the whole module)
// without special-casing which ones happen to exist.
//
// Every decimal string of external origin must go through decimalx.Parse,
// which bounds the coefficient and exponent so a worker does not hang
// materializing the digits (see decimalx's package doc for the attack this
// protects against). A new call site using decimal.NewFromString directly
// would reopen that hole; this helper is meant to be wired into a consuming
// module's own test suite to block it.
func AssertNoDirectNewFromString(t testing.TB, root string, dirs ...string) {
	t.Helper()
	selfDir := filepath.Join(root, "decimalx")

	var offenders []string
	for _, dir := range dirs {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if path == selfDir {
					return filepath.SkipDir
				}
				// Skip hidden directories (.git, .codegraph, ...) so a
				// caller can safely pass "." to scan an entire module.
				if path != base && strings.HasPrefix(info.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path) //nolint:gosec // path comes from filepath.Walk over the caller-supplied root/dirs, the exact tree this helper exists to scan
			if err != nil {
				return err
			}
			if forbiddenCall.Match(data) {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				offenders = append(offenders, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("decimal.NewFromString is forbidden outside decimalx; use decimalx.Parse "+
			"(it bounds the coefficient and exponent so a worker does not hang materializing the digits).\n"+
			"Replace the call with decimalx.Parse and map its error to your module's own sentinel.\n"+
			"Offending files (%d):\n  %s", len(offenders), strings.Join(offenders, "\n  "))
	}
}
