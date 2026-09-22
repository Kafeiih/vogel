// Package decimalxtest provides a structural test helper for consumers of
// decimalx: a guard that fails a test if any .go file outside the decimalx
// package itself calls one of shopspring/decimal's string constructors
// directly, bypassing the bounded parser decimalx.Parse provides.
package decimalxtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// forbiddenConstructorCall detects calls to any of shopspring/decimal's three
// string constructors — NewFromString, RequireFromString, and
// NewFromFormattedString — tolerating spacing gofmt would never produce but
// an editor might (e.g. "decimal .NewFromString").
var forbiddenConstructorCall = regexp.MustCompile(`decimal\s*\.\s*(NewFromString|RequireFromString|NewFromFormattedString)\b`)

// AssertNoDirectNewFromString fails t if any of decimal.NewFromString,
// decimal.RequireFromString, or decimal.NewFromFormattedString — the three
// shopspring/decimal string constructors — is called anywhere in a .go file
// (excluding _test.go files) under root/dir, for every dir in dirs, with
// these exceptions:
//
//   - the decimalx package's own directory (root/decimalx) is never scanned,
//     since it is the only package allowed to call them directly;
//   - hidden directories (.git, .codegraph, ...) are never scanned;
//   - a directory named "vendor" is never scanned, since it holds vendored
//     dependency source, not this repository's own code;
//   - a directory named "testdata" is never scanned, since Go itself treats
//     it as fixture data rather than compiled package source, and it may
//     deliberately contain sample files that use these constructors.
//
// A dir that does not exist under root is silently skipped, so a caller can
// pass a fixed list of top-level directories (or "." for the whole module)
// without special-casing which ones happen to exist.
//
// Every decimal string of external origin must go through decimalx.Parse,
// which bounds the coefficient and exponent so a worker does not hang
// materializing the digits (see decimalx's package doc for the attack this
// protects against). A new call site using any of these three constructors
// directly would reopen that hole; this helper is meant to be wired into a
// consuming module's own test suite to block it.
//
// # What this guard CANNOT detect
//
// This is a source-text regex over unmodified .go files, not a type-aware
// analysis, so it has known blind spots:
//
//   - An aliased import of shopspring/decimal (e.g. `d
//     "github.com/shopspring/decimal"`) changes the call site's spelling to
//     d.NewFromString(...), which this regex does not recognize as the
//     forbidden call.
//   - encoding/json (or any other deserializer) decoding a request body or a
//     bulk-load payload directly into a decimal.Decimal-typed field never
//     calls any of these three constructors at all: the value arrives
//     already built, and UNBOUNDED — this guard has no call site to catch.
//     For that path, callers MUST run decimalx.ValidateBounds (or, for money,
//     decimalx.ValidateAmount) on the decoded value; this guard cannot
//     enforce that requirement structurally, only convention and code review
//     can.
//
// In the opposite direction, this guard is also OVER-inclusive: because it
// matches raw source text rather than parsed Go syntax, it flags a
// forbidden constructor's name even when it appears inside a comment or a
// string literal in a scanned, non-test .go file — not just in an actual
// call expression. A file that legitimately needs to mention
// "decimal.NewFromString" in prose (a comment explaining what NOT to do, for
// example) will fail this guard exactly as if it called it; the fix is to
// reword the mention (or move it into a _test.go file, which is never
// scanned) rather than to treat the failure as a false positive.
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
				// Skip hidden directories (.git, .codegraph, ...), vendor/
				// (vendored dependency source, not this repository's code)
				// and testdata/ (fixture data, not compiled package
				// source), so a caller can safely pass "." to scan an
				// entire module. A dir passed explicitly as one of dirs is
				// still scanned even if it happens to be named this way,
				// exactly like the hidden-directory rule below.
				if path != base && (strings.HasPrefix(info.Name(), ".") || info.Name() == "vendor" || info.Name() == "testdata") {
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
			if forbiddenConstructorCall.Match(data) {
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
		t.Fatalf("decimal.NewFromString/RequireFromString/NewFromFormattedString are forbidden outside decimalx; "+
			"use decimalx.Parse (it bounds the coefficient and exponent so a worker does not hang materializing the digits).\n"+
			"Replace the call with decimalx.Parse and map its error to your module's own sentinel.\n"+
			"Offending files (%d):\n  %s", len(offenders), strings.Join(offenders, "\n  "))
	}
}
