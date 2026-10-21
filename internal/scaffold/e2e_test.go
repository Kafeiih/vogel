package scaffold

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestGenerateRealBoilerplate scaffolds the repository's own
// examples/boilerplate with every component selected.
func TestGenerateRealBoilerplate(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end scaffold of the real boilerplate")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "examples", "boilerplate", ManifestPath)); err != nil {
		t.Skipf("examples/boilerplate/%s does not exist yet: %v", ManifestPath, err)
	}

	opts := Options{
		Name:   "billing-api",
		Owner:  "acme",
		Module: "github.com/acme/billing-api",
		Dir:    filepath.Join(t.TempDir(), "billing-api"),
		From:   root,
		Select: func(m *Manifest) (Selection, error) {
			all := make([]string, 0, len(m.Components))
			for _, c := range m.Components {
				all = append(all, c.Name)
			}
			return Select(m, all, nil, nil)
		},
		SkipTidy: true,
		NoGit:    true,
	}
	res, err := Generate(context.Background(), opts)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(res.Removed) != 0 {
		t.Fatalf("Removed = %v, want every component kept", res.Removed)
	}
	if _, err := os.Stat(filepath.Join(opts.Dir, ".vogel")); !os.IsNotExist(err) {
		t.Fatal(".vogel directory was not removed")
	}

	marker := regexp.MustCompile(`(//|#|--|<!--)\s*vogel:(component|only)`)
	err = filepath.WalkDir(opts.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if marker.Match(data) {
			t.Errorf("%s still contains a vogel marker", path)
		}
		if (strings.HasSuffix(path, ".go") || filepath.Base(path) == "go.mod") &&
			strings.Contains(string(data), "github.com/kafeiih/vogel/examples/boilerplate") {
			t.Errorf("%s still references the boilerplate module path", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	gomod, err := os.ReadFile(filepath.Join(opts.Dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(gomod), "module github.com/acme/billing-api\n") {
		t.Fatalf("go.mod module line not rewritten:\n%s", gomod)
	}
}

// TestGenerateRealBoilerplateDefaultsLeaveOutBPMS scaffolds the real
// boilerplate with the manifest defaults: bpms is off, so nothing of it may
// reach the project.
func TestGenerateRealBoilerplateDefaultsLeaveOutBPMS(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end scaffold of the real boilerplate")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "examples", "boilerplate", ManifestPath)); err != nil {
		t.Skipf("examples/boilerplate/%s does not exist yet: %v", ManifestPath, err)
	}

	dir := filepath.Join(t.TempDir(), "billing-api")
	res, err := Generate(context.Background(), Options{
		Name:     "billing-api",
		Owner:    "acme",
		Module:   "github.com/acme/billing-api",
		Dir:      dir,
		From:     root,
		SkipTidy: true,
		NoGit:    true,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got := strings.Join(res.Removed, ","); got != "bpms" {
		t.Fatalf("Removed = %s, want bpms only", got)
	}
	for _, p := range []string{
		"internal/domain/process",
		"internal/application/process",
		"internal/infrastructure/bpms",
		"internal/infrastructure/config/config_bpms_test.go",
		"internal/interfaces/http/handler/process_handler.go",
		"internal/interfaces/http/rolematrix_bpms_test.go",
		"cerbos/policies/resource_bpms_process.yaml",
		"cerbos/policies/resource_bpms_case.yaml",
		"cerbos/tests/resource_bpms_test.yaml",
		"docs/BPMS.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s was not removed", p)
		}
	}
	leaks := []string{"GOTENBERG", "gotenberg", "BPMS", "infrastructure/bpms", "kafeiih/vogel/workflow", "kafeiih/vogel/action", "kafeiih/vogel/document", "document_db_version",
		"bpms_db_version", "bpms:", "domain/process", "application/process", "ProcessHandler", "PROFILE_HOLDERS"}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, leak := range leaks {
			if strings.Contains(string(data), leak) {
				t.Errorf("%s contains %q although bpms is off", path, leak)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
