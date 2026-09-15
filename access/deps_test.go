package access

import (
	"os/exec"
	"strings"
	"testing"
)

// TestDeps_NeverPullsHTTPOrChi guards the package doc's central claim: a use
// case that has never heard of HTTP -- a worker, a CLI, a queue consumer --
// must still be able to call access.Guard.Check. `go list -deps` reports
// every package this one pulls in, transitively; if chi, httpx, or net/http
// ever show up there, this package stopped being safe to import from
// outside a web server, silently.
func TestDeps_NeverPullsHTTPOrChi(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	cmd := exec.Command("go", "list", "-deps", ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps .: %v\n%s", err, out)
	}

	deps := string(out)
	forbidden := []string{
		"github.com/go-chi/chi",
		"github.com/kafeiih/vogel/httpx",
		"net/http",
	}
	for _, f := range forbidden {
		if strings.Contains(deps, f) {
			t.Errorf("go list -deps . contains forbidden dependency %q", f)
		}
	}
}
