package postgres

import (
	"strings"
	"testing"
)

// prefixed returns cols with every column qualified by alias, keeping the
// layout (commas, line breaks, indentation) of cols.
func prefixed(alias, cols string) string {
	fields := strings.Split(cols, ",")
	for i, f := range fields {
		name := strings.TrimLeft(f, " \n")
		fields[i] = f[:len(f)-len(name)] + alias + "." + name
	}
	return strings.Join(fields, ",")
}

// The prefixed lists are hand-written constants (queries embed them in const
// strings), so this test keeps them in step with the lists caseDest and
// tokenDest scan.
func TestPrefixedColumnsMatchTheirBaseLists(t *testing.T) {
	if got, want := prefixedCaseColumns, prefixed("c", caseColumns); got != want {
		t.Errorf("prefixedCaseColumns drifted from caseColumns:\n got %q\nwant %q", got, want)
	}
	if got, want := prefixedTokenColumns, prefixed("t", tokenColumns); got != want {
		t.Errorf("prefixedTokenColumns drifted from tokenColumns:\n got %q\nwant %q", got, want)
	}
}
