package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kafeiih/vogel/internal/scaffold"
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
	code := run(context.Background(), args, strings.NewReader(stdin), &out, &errOut, interactive)
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
	for _, want := range []string{"GitHub owner", "Keep the worker? [Y/n]", "Please answer y or n (s/si also mean yes).", "Keep the bundled Cerbos PDP? [Y/n]", "Keep S3 storage? [y/N]", "kept:    cerbos, storage"} {
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

// blockingStdin is a terminal nobody types into: reads block until the test
// ends.
func blockingStdin(t *testing.T) io.Reader {
	t.Helper()
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	return r
}

func TestAskYesNoHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := askYesNo(ctx, newLineReader(blockingStdin(t)), io.Discard, scaffold.Component{Name: "worker"})
		done <- err
	}()
	time.AfterFunc(50*time.Millisecond, cancel)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("askYesNo() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("askYesNo() kept blocking after the context was canceled")
	}
}

func TestAskOwnerHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := askOwner(ctx, newLineReader(blockingStdin(t)), io.Discard)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("askOwner() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("askOwner() kept blocking after the context was canceled")
	}
}

func TestInterruptAtPromptLeavesNoTarget(t *testing.T) {
	root := fakeVogelRoot(t)
	dir := filepath.Join(t.TempDir(), "billing")
	ctx, cancel := context.WithCancel(context.Background())
	var out, errOut syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"--owner", "acme", "--from", root, "--dir", dir, "--no-git", "--skip-tidy", "billing"}, blockingStdin(t), &out, &errOut, true)
	}()
	// Cancel only once the component prompt is showing, however slow the
	// runner is at copying the template.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "Keep the worker?") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case code := <-done:
		if code != exitInterrupted {
			t.Fatalf("exit = %d, want %d; stderr:\n%s", code, exitInterrupted, errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run kept waiting at the prompt after the interrupt")
	}
	if !strings.Contains(out.String(), "Keep the worker?") {
		t.Fatalf("the interrupt did not happen at a component prompt; stdout:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "interrupted") {
		t.Fatalf("stderr does not report the interrupt:\n%s", errOut.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("an interrupt at a prompt left the target directory behind")
	}
}

// syncBuffer is a bytes.Buffer safe to read while run writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestInterruptedNamesLeftoverProject pins the interrupt message: when the
// interrupt lands after go.mod editing, Generate keeps the partial project
// for inspection and the message must say where it is.
func TestInterruptedNamesLeftoverProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "billing")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	if code := interrupted(&errOut, dir); code != exitInterrupted {
		t.Fatalf("exit = %d, want %d", code, exitInterrupted)
	}
	if !strings.Contains(errOut.String(), dir) || !strings.Contains(errOut.String(), "left") {
		t.Fatalf("message does not name the partial project %s:\n%s", dir, errOut.String())
	}

	errOut.Reset()
	interrupted(&errOut, filepath.Join(t.TempDir(), "missing"))
	if strings.Contains(errOut.String(), "left") {
		t.Fatalf("message mentions a leftover project that does not exist:\n%s", errOut.String())
	}
}
