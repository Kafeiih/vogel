package decimalxtest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kafeiih/vogel/decimalx/decimalxtest"
)

// fakeTB is a minimal testing.TB double that records whether Fatalf was
// called, so the failure path of AssertNoDirectNewFromString can be
// exercised without failing the outer test that drives it. Embedding the
// unexported testing.TB interface satisfies its private() marker method;
// AssertNoDirectNewFromString only ever calls Helper and Fatalf on the TB it
// receives, so the embedded nil interface is never invoked.
type fakeTB struct {
	testing.TB
	failed  bool
	message string
}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.failed = true
	f.message = fmt.Sprintf(format, args...)
}

// TestAssertNoDirectNewFromString_CleanTreePasses verifies a directory tree
// with no decimal.NewFromString call does not fail.
func TestAssertNoDirectNewFromString_CleanTreePasses(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "app", "handler.go"), `package app

import "github.com/kafeiih/vogel/decimalx"

func Parse(s string) error {
	_, err := decimalx.Parse(s)
	return err
}
`)

	fake := &fakeTB{}
	decimalxtest.AssertNoDirectNewFromString(fake, root, "app")

	if fake.failed {
		t.Fatalf("AssertNoDirectNewFromString failed on a clean tree: %s", fake.message)
	}
}

// TestAssertNoDirectNewFromString_OffendingFileFails verifies a direct
// decimal.NewFromString call outside decimalx is caught.
func TestAssertNoDirectNewFromString_OffendingFileFails(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "app", "handler.go"), `package app

import "github.com/shopspring/decimal"

func Parse(s string) (decimal.Decimal, error) {
	return decimal.NewFromString(s)
}
`)

	fake := &fakeTB{}
	decimalxtest.AssertNoDirectNewFromString(fake, root, "app")

	if !fake.failed {
		t.Fatal("AssertNoDirectNewFromString did not fail on a file calling decimal.NewFromString directly")
	}
}

// TestAssertNoDirectNewFromString_RequireFromStringFails verifies the guard
// also catches decimal.RequireFromString, not just decimal.NewFromString:
// both bypass decimalx.Parse's bounds equally.
func TestAssertNoDirectNewFromString_RequireFromStringFails(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "app", "handler.go"), `package app

import "github.com/shopspring/decimal"

func Parse(s string) decimal.Decimal {
	return decimal.RequireFromString(s)
}
`)

	fake := &fakeTB{}
	decimalxtest.AssertNoDirectNewFromString(fake, root, "app")

	if !fake.failed {
		t.Fatal("AssertNoDirectNewFromString did not fail on a file calling decimal.RequireFromString directly")
	}
}

// TestAssertNoDirectNewFromString_NewFromFormattedStringFails verifies the
// guard also catches decimal.NewFromFormattedString, the third
// shopspring/decimal string constructor that bypasses decimalx.Parse.
func TestAssertNoDirectNewFromString_NewFromFormattedStringFails(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "app", "handler.go"), `package app

import (
	"regexp"

	"github.com/shopspring/decimal"
)

func Parse(s string) (decimal.Decimal, error) {
	return decimal.NewFromFormattedString(s, regexp.MustCompile("[^0-9.]"))
}
`)

	fake := &fakeTB{}
	decimalxtest.AssertNoDirectNewFromString(fake, root, "app")

	if !fake.failed {
		t.Fatal("AssertNoDirectNewFromString did not fail on a file calling decimal.NewFromFormattedString directly")
	}
}

// TestAssertNoDirectNewFromString_ToleratesSpacing verifies the regex
// catches the call even with unusual (non-gofmt) spacing around the dot, as
// an editor might introduce before gofmt runs.
func TestAssertNoDirectNewFromString_ToleratesSpacing(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "app", "handler.go"), `package app

import "github.com/shopspring/decimal"

func Parse(s string) (decimal.Decimal, error) {
	return decimal .NewFromString(s)
}
`)

	fake := &fakeTB{}
	decimalxtest.AssertNoDirectNewFromString(fake, root, "app")

	if !fake.failed {
		t.Fatal("AssertNoDirectNewFromString did not catch decimal.NewFromString with unusual spacing")
	}
}

// TestAssertNoDirectNewFromString_IgnoresTestFiles verifies _test.go files
// are not scanned: crucible's own decimalutil tests construct hostile
// decimals via decimal.RequireFromString, and other packages' tests may
// reasonably want to build a decimal.Decimal directly without going through
// decimalx.
func TestAssertNoDirectNewFromString_IgnoresTestFiles(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "app", "handler_test.go"), `package app

import "github.com/shopspring/decimal"

func mustParse(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}
`)

	fake := &fakeTB{}
	decimalxtest.AssertNoDirectNewFromString(fake, root, "app")

	if fake.failed {
		t.Fatalf("AssertNoDirectNewFromString scanned a _test.go file: %s", fake.message)
	}
}

// TestAssertNoDirectNewFromString_ExcludesDecimalxItself verifies the
// decimalx package's own directory (root/decimalx) is never scanned, since
// it is the one package allowed to call decimal.NewFromString.
func TestAssertNoDirectNewFromString_ExcludesDecimalxItself(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "decimalx", "decimalx.go"), `package decimalx

import "github.com/shopspring/decimal"

func Parse(s string) (decimal.Decimal, error) {
	return decimal.NewFromString(s)
}
`)

	fake := &fakeTB{}
	decimalxtest.AssertNoDirectNewFromString(fake, root, "decimalx")

	if fake.failed {
		t.Fatalf("AssertNoDirectNewFromString scanned decimalx's own directory: %s", fake.message)
	}
}

// TestAssertNoDirectNewFromString_MissingDirIsSkipped verifies a named dir
// that does not exist under root is silently skipped rather than failing.
func TestAssertNoDirectNewFromString_MissingDirIsSkipped(t *testing.T) {
	root := t.TempDir()

	fake := &fakeTB{}
	decimalxtest.AssertNoDirectNewFromString(fake, root, "does-not-exist")

	if fake.failed {
		t.Fatalf("AssertNoDirectNewFromString failed on a missing directory: %s", fake.message)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
