package scaffold

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureBinary = "\x00\x01// vogel:component bogus begin\n"

// fakeVogelRoot lays out testdata/template as <root>/examples/boilerplate so
// it can be used with Options.From. The root is not a git repository, so the
// copy walks the directory.
func fakeVogelRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/kafeiih/vogel\n\ngo 1.26.7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bp := filepath.Join(root, "examples", "boilerplate")
	err := filepath.WalkDir("testdata/template", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("testdata/template", path)
		target := filepath.Join(bp, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(bp, "scripts", "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(bp, "assets", "logo.bin"), fixtureBinary, 0o644)
	return root
}

func selectWithout(without ...string) func(*Manifest) (Selection, error) {
	return func(m *Manifest) (Selection, error) { return Select(m, nil, without, nil) }
}

func baseOptions(t *testing.T, root string) Options {
	t.Helper()
	return Options{
		Name:     "billing-api",
		Owner:    "Acme",
		Module:   "github.com/acme/billing-api",
		Dir:      filepath.Join(t.TempDir(), "billing-api"),
		From:     root,
		Select:   selectWithout(),
		SkipTidy: true,
		NoGit:    true,
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertNoMarkers(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, ".bin") {
			return err
		}
		if strings.Contains(readFile(t, path), "vogel:") {
			t.Errorf("%s still contains a vogel: marker", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGenerateFromLocalDefaults(t *testing.T) {
	root := fakeVogelRoot(t)
	opts := baseOptions(t, root)
	res, err := Generate(context.Background(), opts)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	dir := opts.Dir

	if got := strings.Join(res.Kept, ","); got != "worker,notification,cerbos" {
		t.Fatalf("Kept = %s", got)
	}
	if got := strings.Join(res.Removed, ","); got != "storage" {
		t.Fatalf("Removed = %s", got)
	}
	if res.Version != "v0.9.0" {
		t.Fatalf("Version = %q, want the boilerplate's require v0.9.0", res.Version)
	}
	if res.LocalReplace != root {
		t.Fatalf("LocalReplace = %q, want %q", res.LocalReplace, root)
	}
	if _, err := os.Stat(filepath.Join(dir, ".vogel")); !os.IsNotExist(err) {
		t.Fatal(".vogel was not removed")
	}
	assertNoMarkers(t, dir)

	wantMain := `package main

import (
	"fmt"

	"github.com/acme/billing-api/internal/app"
)

func main() {
	fmt.Println(app.Name())
	fmt.Println("worker enabled")
	fmt.Println("notifications enabled")
}
`
	if got := readFile(t, filepath.Join(dir, "cmd", "api", "main.go")); got != wantMain {
		t.Fatalf("cmd/api/main.go =\n%s\nwant\n%s", got, wantMain)
	}
	gomod := readFile(t, filepath.Join(dir, "go.mod"))
	for _, want := range []string{"module github.com/acme/billing-api\n", "github.com/kafeiih/vogel v0.9.0", "replace github.com/kafeiih/vogel => " + root} {
		if !strings.Contains(gomod, want) {
			t.Fatalf("go.mod missing %q:\n%s", want, gomod)
		}
	}
	readme := readFile(t, filepath.Join(dir, "README.md"))
	if !strings.Contains(readme, "github.com/acme/billing-api") || strings.Contains(readme, "inside the vogel repository") || !strings.Contains(readme, "## Cerbos") {
		t.Fatalf("README.md not rewritten as expected:\n%s", readme)
	}
	compose := readFile(t, filepath.Join(dir, "deploy", "docker-compose.yaml"))
	if !strings.Contains(compose, "image: ghcr.io/acme/billing-api:latest") || !strings.Contains(compose, "image: ghcr.io/acme/billing-api-worker:latest") || strings.Contains(compose, "kafeiih") {
		t.Fatalf("compose not rewritten:\n%s", compose)
	}
	if got := readFile(t, filepath.Join(dir, "cerbos", "config.yaml")); got != "name: billing-api-pdp\n" {
		t.Fatalf("cerbos/config.yaml = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "Makefile")); !strings.HasPrefix(got, "DB ?= billing_api\n") || !strings.Contains(got, "go run ./cmd/worker") {
		t.Fatalf("Makefile = %q", got)
	}
	if fi, err := os.Stat(filepath.Join(dir, "scripts", "run.sh")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("run.sh lost its executable bit: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "assets", "logo.bin")); got != fixtureBinary {
		t.Fatalf("binary file was modified: %q", got)
	}
}

func TestGenerateDeselectsComponents(t *testing.T) {
	root := fakeVogelRoot(t)
	opts := baseOptions(t, root)
	opts.Select = selectWithout("worker", "cerbos")
	res, err := Generate(context.Background(), opts)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	dir := opts.Dir
	if got := strings.Join(res.Cascaded, ","); got != "notification" {
		t.Fatalf("Cascaded = %s", got)
	}
	for _, gone := range []string{"cmd/worker", "cerbos"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed", gone)
		}
	}
	main := readFile(t, filepath.Join(dir, "cmd", "api", "main.go"))
	if strings.Contains(main, "worker enabled") || strings.Contains(main, "notifications enabled") {
		t.Fatalf("worker blocks left in main.go:\n%s", main)
	}
	compose := readFile(t, filepath.Join(dir, "deploy", "docker-compose.yaml"))
	if strings.Contains(compose, "worker") || strings.Contains(compose, "cerbos") || !strings.Contains(compose, "ghcr.io/acme/billing-api:latest") {
		t.Fatalf("compose = %s", compose)
	}
	if got := readFile(t, filepath.Join(dir, "migrations", "001_init.sql")); got != "CREATE TABLE things (id int);\n" {
		t.Fatalf("sql = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "Makefile")); strings.Contains(got, "worker") {
		t.Fatalf("Makefile = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "README.md")); strings.Contains(got, "Cerbos") {
		t.Fatalf("README = %q", got)
	}
	assertNoMarkers(t, dir)
}

func TestGenerateFromLocalWithVersion(t *testing.T) {
	opts := baseOptions(t, fakeVogelRoot(t))
	opts.Version = "v0.10.0"
	res, err := Generate(context.Background(), opts)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	gomod := readFile(t, filepath.Join(opts.Dir, "go.mod"))
	if !strings.Contains(gomod, "github.com/kafeiih/vogel v0.10.0") || res.Version != "v0.10.0" {
		t.Fatalf("go.mod does not require v0.10.0:\n%s", gomod)
	}
}

// fixtureTarball packs testdata/template the way codeload serves a tag.
func fixtureTarball(t *testing.T, top string) []byte {
	t.Helper()
	root := fakeVogelRoot(t)
	var entries []tarEntry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		name := top + "/" + filepath.ToSlash(rel)
		if d.IsDir() {
			entries = append(entries, tarEntry{name: name + "/", typeflag: tar.TypeDir, mode: 0o755})
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, tarEntry{name: name, body: readFile(t, path), mode: int64(info.Mode().Perm())})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return buildTarball(t, entries)
}

func tagServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	data := fixtureTarball(t, "vogel-"+strings.TrimPrefix(version, "v"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/kafeiih/vogel/tar.gz/refs/tags/"+version {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGenerateFromTag(t *testing.T) {
	srv := tagServer(t, "v0.10.0")
	opts := baseOptions(t, "")
	opts.From = ""
	opts.Version = "v0.10.0"
	opts.Fetcher = &Fetcher{BaseURL: srv.URL, Client: srv.Client()}
	res, err := Generate(context.Background(), opts)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	gomod := readFile(t, filepath.Join(opts.Dir, "go.mod"))
	if strings.Contains(gomod, "replace") || !strings.Contains(gomod, "github.com/kafeiih/vogel v0.10.0") {
		t.Fatalf("go.mod in tag mode:\n%s", gomod)
	}
	if res.LocalReplace != "" {
		t.Fatalf("LocalReplace = %q in tag mode", res.LocalReplace)
	}
	if fi, err := os.Stat(filepath.Join(opts.Dir, "scripts", "run.sh")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("run.sh lost its executable bit: %v", err)
	}
	assertNoMarkers(t, opts.Dir)
}

func TestGenerateRejectsTagBelowMinimum(t *testing.T) {
	srv := tagServer(t, "v0.9.5")
	opts := baseOptions(t, "")
	opts.From = ""
	opts.Version = "v0.9.5"
	opts.Fetcher = &Fetcher{BaseURL: srv.URL, Client: srv.Client()}
	_, err := Generate(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "v0.10.0") {
		t.Fatalf("Generate() error = %v, want minimum version error", err)
	}
	if _, err := os.Stat(opts.Dir); !os.IsNotExist(err) {
		t.Fatal("target directory should be cleaned up after a failure before go.mod edits")
	}
}

func TestGenerateTemplateDrift(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(t *testing.T, bp string)
		wantMsg string
	}{
		{
			name: "replacement source missing",
			mutate: func(t *testing.T, bp string) {
				mustWrite(t, filepath.Join(bp, "Makefile"), "DB ?= other\n", 0o644)
			},
			wantMsg: `"boilerplate_db" not found in Makefile`,
		},
		{
			name: "replacement file missing",
			mutate: func(t *testing.T, bp string) {
				if err := os.Remove(filepath.Join(bp, "Makefile")); err != nil {
					t.Fatal(err)
				}
			},
			wantMsg: "Makefile",
		},
		{
			name: "component path missing",
			mutate: func(t *testing.T, bp string) {
				if err := os.RemoveAll(filepath.Join(bp, "cmd", "worker")); err != nil {
					t.Fatal(err)
				}
			},
			wantMsg: "cmd/worker",
		},
		{
			name: "unbalanced marker names file and line",
			mutate: func(t *testing.T, bp string) {
				mustWrite(t, filepath.Join(bp, "deploy", "extra.yaml"), "a: 1\n# vogel:component cerbos begin\n", 0o644)
			},
			wantMsg: "deploy/extra.yaml:2",
		},
		{
			name: "unknown component in marker",
			mutate: func(t *testing.T, bp string) {
				mustWrite(t, filepath.Join(bp, "x.sql"), "-- vogel:component billing begin\n-- vogel:component billing end\n", 0o644)
			},
			wantMsg: `x.sql:1: unknown component "billing"`,
		},
		{
			name: "gofmt detects a misplaced block",
			mutate: func(t *testing.T, bp string) {
				body := "package app\n\nfunc f() {\n\ta := 1\n\n\t// vogel:component cerbos begin\n\t_ = a\n\t// vogel:component cerbos end\n\n\t_ = a\n}\n"
				mustWrite(t, filepath.Join(bp, "internal", "app", "f.go"), body, 0o644)
			},
			wantMsg: "internal/app/f.go",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := fakeVogelRoot(t)
			tt.mutate(t, filepath.Join(root, "examples", "boilerplate"))
			opts := baseOptions(t, root)
			opts.Select = selectWithout("cerbos")
			_, err := Generate(context.Background(), opts)
			if err == nil {
				t.Fatal("Generate() error = nil, want drift error")
			}
			if !strings.Contains(filepath.ToSlash(err.Error()), tt.wantMsg) {
				t.Fatalf("error %q does not contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestGenerateTargetDirectory(t *testing.T) {
	root := fakeVogelRoot(t)

	opts := baseOptions(t, root)
	mustWrite(t, filepath.Join(opts.Dir, "keep.txt"), "x", 0o644)
	_, err := Generate(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("non-empty target error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(opts.Dir, "keep.txt")); err != nil {
		t.Fatal("pre-existing user file was touched")
	}

	opts = baseOptions(t, root)
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(context.Background(), opts); err != nil {
		t.Fatalf("empty target error = %v", err)
	}
}

func TestGenerateValidatesOptions(t *testing.T) {
	root := fakeVogelRoot(t)
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{"bad name", func(o *Options) { o.Name = "Bad_Name" }},
		{"bad owner", func(o *Options) { o.Owner = "-x" }},
		{"bad module", func(o *Options) { o.Module = "billing" }},
		{"bad version", func(o *Options) { o.Version = "latest" }},
		{"no source", func(o *Options) { o.From = ""; o.Version = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := baseOptions(t, root)
			tt.mutate(&opts)
			_, err := Generate(context.Background(), opts)
			var ue *UsageError
			if !errors.As(err, &ue) {
				t.Fatalf("Generate() error = %v, want *UsageError", err)
			}
		})
	}
}

func isolateGit(t *testing.T, config string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	cfg := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(cfg, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestGenerateGitCommit(t *testing.T) {
	isolateGit(t, "[user]\n\tname = Test\n\temail = test@example.com\n[commit]\n\tgpgsign = false\n")
	opts := baseOptions(t, fakeVogelRoot(t))
	opts.NoGit = false
	res, err := Generate(context.Background(), opts)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !res.Committed {
		t.Fatalf("Committed = false (%s)", res.GitNote)
	}
	out, err := exec.Command("git", "-C", opts.Dir, "log", "--format=%s%n%b", "-1").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "chore: scaffold billing-api from vogel v0.9.0" {
		t.Fatalf("commit message = %q", got)
	}
	branch, _ := exec.Command("git", "-C", opts.Dir, "branch", "--show-current").Output()
	if strings.TrimSpace(string(branch)) != "main" {
		t.Fatalf("branch = %q, want main", branch)
	}
	status, _ := exec.Command("git", "-C", opts.Dir, "status", "--porcelain").Output()
	if len(bytes.TrimSpace(status)) != 0 {
		t.Fatalf("working tree not clean after commit:\n%s", status)
	}
}

func TestGenerateGitWithoutIdentity(t *testing.T) {
	isolateGit(t, "")
	opts := baseOptions(t, fakeVogelRoot(t))
	opts.NoGit = false
	res, err := Generate(context.Background(), opts)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if res.Committed || !strings.Contains(res.GitNote, "user.email") {
		t.Fatalf("Committed = %v, GitNote = %q", res.Committed, res.GitNote)
	}
	if _, err := os.Stat(filepath.Join(opts.Dir, ".git")); err != nil {
		t.Fatal("git init should still have run")
	}
}

func TestGenerateRunsTidy(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go mod tidy")
	}
	opts := baseOptions(t, fakeVogelRoot(t))
	opts.SkipTidy = false
	if _, err := Generate(context.Background(), opts); err != nil {
		t.Fatalf("Generate() with tidy error = %v", err)
	}
}

func TestGenerateTidyFailureLeavesProject(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go mod tidy")
	}
	root := fakeVogelRoot(t)
	mustWrite(t, filepath.Join(root, "examples", "boilerplate", "internal", "app", "dep.go"),
		"package app\n\nimport _ \"github.com/kafeiih/vogel/doesnotexist\"\n", 0o644)
	opts := baseOptions(t, root)
	opts.SkipTidy = false
	_, err := Generate(context.Background(), opts)
	var ce *CommandError
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "run `go mod tidy` there") {
		t.Fatalf("Generate() error = %v, want a go mod tidy CommandError with a hint", err)
	}
	if _, err := os.Stat(filepath.Join(opts.Dir, "go.mod")); err != nil {
		t.Fatal("the project should be left in place for inspection after a tidy failure")
	}
}

func TestCommandErrorTruncatesOutput(t *testing.T) {
	lines := make([]string, 0, 50)
	for i := range 50 {
		lines = append(lines, "line "+string(rune('a'+i%26)))
	}
	err := &CommandError{Cmd: "go mod tidy", Output: strings.Join(lines, "\n"), Err: errors.New("exit status 1")}
	msg := err.Error()
	if !strings.Contains(msg, "30 earlier lines omitted") || strings.Count(msg, "\n") > maxErrorLines+2 {
		t.Fatalf("output not truncated:\n%s", msg)
	}
}

func TestGenerateGoModCheckFailureCleansUp(t *testing.T) {
	tests := []struct {
		name    string
		gomod   string
		wantMsg string
	}{
		{"module path does not match the manifest", "module example.com/other\n\ngo 1.26.7\n", `go.mod declares module "example.com/other"`},
		{"unparsable go.mod", "this is not a go.mod file\n", "go mod edit -json"},
	}
	for _, tt := range tests {
		for _, preexisting := range []bool{false, true} {
			name := tt.name
			if preexisting {
				name += " in a pre-existing empty directory"
			}
			t.Run(name, func(t *testing.T) {
				root := fakeVogelRoot(t)
				mustWrite(t, filepath.Join(root, "examples", "boilerplate", "go.mod"), tt.gomod, 0o644)
				opts := baseOptions(t, root)
				if preexisting {
					if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				_, err := Generate(context.Background(), opts)
				if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
					t.Fatalf("Generate() error = %v, want it to contain %q", err, tt.wantMsg)
				}
				entries, statErr := os.ReadDir(opts.Dir)
				switch {
				case preexisting && statErr != nil:
					t.Fatalf("pre-existing target directory was removed: %v", statErr)
				case preexisting && len(entries) > 0:
					t.Fatalf("pre-existing target directory was not emptied: %d entries left", len(entries))
				case !preexisting && !os.IsNotExist(statErr):
					t.Fatal("target directory should be removed after a failure before go.mod is edited")
				}
			})
		}
	}
}
