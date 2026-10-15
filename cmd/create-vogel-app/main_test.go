package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeVogelRoot lays the scaffold fixture template out as
// <root>/examples/boilerplate.
func fakeVogelRoot(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "..", "internal", "scaffold", "testdata", "template")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/kafeiih/vogel\n\ngo 1.26.7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bp := filepath.Join(root, "examples", "boilerplate")
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
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
	return root
}

type result struct {
	code           int
	stdout, stderr string
}

func runCLI(t *testing.T, stdin string, interactive bool, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errOut, interactive)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func TestHelp(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		r := runCLI(t, "", false, arg)
		if r.code != exitOK {
			t.Fatalf("%s exit = %d", arg, r.code)
		}
		for _, want := range []string{"Usage: create-vogel-app", "--owner", "--module", "--dir", "--version", "--from", "--with", "--without", "--yes", "--no-git", "--skip-tidy", "--skip-checks", "Exit codes"} {
			if !strings.Contains(r.stdout, want) {
				t.Fatalf("%s output missing %q:\n%s", arg, want, r.stdout)
			}
		}
	}
}

func TestUsageErrors(t *testing.T) {
	root := fakeVogelRoot(t)
	dir := filepath.Join(t.TempDir(), "x")
	base := []string{"--from", root, "--dir", dir, "--no-git", "--skip-tidy"}
	tests := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{"missing name", []string{"--owner", "acme"}, "missing <name>"},
		{"two names", []string{"--owner", "acme", "a", "b"}, "exactly one"},
		{"invalid name", []string{"--owner", "acme", "Bad_Name"}, "invalid name"},
		{"unknown flag", []string{"--bogus", "billing"}, "bogus"},
		{"owner required without a terminal", append([]string{"billing"}, base...), "--owner is required"},
		{"owner required with --yes", append([]string{"--yes", "billing"}, base...), "--owner is required"},
		{"with and without", append([]string{"--owner", "acme", "--with", "cerbos", "--without", "cerbos", "billing"}, base...), "both --with and --without"},
		{"unknown component", append([]string{"--owner", "acme", "--with", "billing", "billing"}, base...), `unknown component "billing"`},
		{"with whose requirement is excluded", append([]string{"--owner", "acme", "--with", "notification", "--without", "worker", "billing"}, base...), "requires worker"},
		{"invalid module", append([]string{"--owner", "acme", "--module", "billing", "billing"}, base...), "invalid module path"},
		{"invalid version", append([]string{"--owner", "acme", "--version", "latest", "billing"}, base...), "invalid version"},
		{"development build without --version or --from", []string{"--owner", "acme", "billing"}, "--from"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runCLI(t, "", false, tt.args...)
			if r.code != exitUsage {
				t.Fatalf("exit = %d, want %d; stderr:\n%s", r.code, exitUsage, r.stderr)
			}
			if !strings.Contains(r.stderr, tt.wantMsg) {
				t.Fatalf("stderr %q does not contain %q", r.stderr, tt.wantMsg)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("a usage error left the target directory behind")
			}
		})
	}
}

func TestNonInteractiveRun(t *testing.T) {
	root := fakeVogelRoot(t)
	dir := filepath.Join(t.TempDir(), "billing")
	r := runCLI(t, "", false, "billing", "--owner", "Acme", "--from", root, "--dir", dir, "--without", "worker", "--with", "storage", "--no-git", "--skip-tidy")
	if r.code != exitOK {
		t.Fatalf("exit = %d; stderr:\n%s", r.code, r.stderr)
	}
	for _, want := range []string{
		"module:  github.com/acme/billing",
		"kept:    cerbos, storage",
		"removed: worker, notification",
		"notification was removed because it requires worker",
		"cd " + dir,
		"cp deploy/env.example deploy/.env",
		"make dev-infra",
		"gh repo create Acme/billing --private --source=. --remote=origin --push",
		".github/workflows/release.yaml",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
	if !strings.Contains(r.stderr, "local vogel checkout "+root) || !strings.Contains(r.stderr, "-dropreplace=github.com/kafeiih/vogel") {
		t.Fatalf("stderr missing the --from warning:\n%s", r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd", "worker")); !os.IsNotExist(err) {
		t.Fatal("cmd/worker should be removed")
	}
	compose, err := os.ReadFile(filepath.Join(dir, "deploy", "docker-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "ghcr.io/acme/billing:latest") {
		t.Fatalf("image name not lowercased into ghcr.io/acme/billing:\n%s", compose)
	}
}

func TestInteractiveRun(t *testing.T) {
	root := fakeVogelRoot(t)
	dir := filepath.Join(t.TempDir(), "billing")
	// owner (first answer invalid), worker: maybe (re-asked) then n,
	// cerbos: default (yes), storage: y. notification is never asked.
	stdin := "-bad\nacme\nmaybe\nn\n\ny\n"
	r := runCLI(t, stdin, true, "billing", "--from", root, "--dir", dir, "--no-git", "--skip-tidy")
	if r.code != exitOK {
		t.Fatalf("exit = %d; stderr:\n%s\nstdout:\n%s", r.code, r.stderr, r.stdout)
	}
	for _, want := range []string{"GitHub owner", "Keep the worker? [Y/n]", "Please answer y or n.", "Keep the bundled Cerbos PDP? [Y/n]", "Keep S3 storage? [y/N]", "kept:    cerbos, storage"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "Keep notifications?") {
		t.Fatal("notification was asked although worker was declined")
	}
}

func TestYesSkipsPrompts(t *testing.T) {
	root := fakeVogelRoot(t)
	dir := filepath.Join(t.TempDir(), "billing")
	r := runCLI(t, "", true, "--yes", "--owner", "acme", "--from", root, "--dir", dir, "--no-git", "--skip-tidy", "billing")
	if r.code != exitOK {
		t.Fatalf("exit = %d; stderr:\n%s", r.code, r.stderr)
	}
	if strings.Contains(r.stdout, "[Y/n]") || !strings.Contains(r.stdout, "kept:    worker, notification, cerbos") {
		t.Fatalf("--yes should use defaults without asking:\n%s", r.stdout)
	}
}

func TestRuntimeFailureExitCode(t *testing.T) {
	root := fakeVogelRoot(t)
	if err := os.WriteFile(filepath.Join(root, "examples", "boilerplate", "Makefile"), []byte("no db here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t, "", false, "--owner", "acme", "--from", root, "--dir", filepath.Join(t.TempDir(), "x"), "--no-git", "--skip-tidy", "billing")
	if r.code != exitFailure || !strings.Contains(r.stderr, "not found in Makefile") {
		t.Fatalf("exit = %d, stderr = %q", r.code, r.stderr)
	}
}
