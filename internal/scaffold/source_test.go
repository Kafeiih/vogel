package scaffold

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	linkname string
}

func buildTarball(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// codeload tarballs start with a pax global header carrying the commit id.
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "abc"}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: e.typeflag, Linkname: e.linkname}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Mode == 0 {
			hdr.Mode = 0o644
		}
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func goodEntries() []tarEntry {
	return []tarEntry{
		{name: "vogel-0.10.0/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "vogel-0.10.0/go.mod", body: "module github.com/kafeiih/vogel\n"},
		{name: "vogel-0.10.0/examples/boilerplate/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "vogel-0.10.0/examples/boilerplate/go.mod", body: "module x\n"},
		{name: "vogel-0.10.0/examples/boilerplate/scripts/run.sh", body: "#!/bin/sh\n", mode: 0o755},
		{name: "vogel-0.10.0/examples/boilerplate/empty/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "vogel-0.10.0/examples/api/main.go", body: "package main\n"},
	}
}

func TestExtractBoilerplate(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	data := buildTarball(t, goodEntries())
	if err := ExtractBoilerplate(bytes.NewReader(data), dest, 1<<20); err != nil {
		t.Fatalf("ExtractBoilerplate() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "go.mod"))
	if err != nil || string(got) != "module x\n" {
		t.Fatalf("go.mod = %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(dest, "scripts", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("run.sh mode = %v, want executable bit kept", info.Mode())
	}
	if fi, err := os.Stat(filepath.Join(dest, "empty")); err != nil || !fi.IsDir() {
		t.Fatalf("empty dir not created: %v", err)
	}
	for _, outside := range []string{"main.go", "api", "examples"} {
		if _, err := os.Stat(filepath.Join(dest, outside)); !os.IsNotExist(err) {
			t.Fatalf("entry outside the boilerplate leaked: %s", outside)
		}
	}
}

func TestExtractBoilerplateRejects(t *testing.T) {
	tests := []struct {
		name    string
		extra   []tarEntry
		limit   int64
		wantMsg string
	}{
		{"parent traversal", []tarEntry{{name: "vogel-0.10.0/examples/boilerplate/../../../evil", body: "x"}}, 1 << 20, "unsafe"},
		{"absolute path", []tarEntry{{name: "/etc/passwd", body: "x"}}, 1 << 20, "unsafe"},
		{"escaping symlink", []tarEntry{{name: "vogel-0.10.0/examples/boilerplate/link", typeflag: tar.TypeSymlink, linkname: "../../../../etc/passwd"}}, 1 << 20, "symlink"},
		{"absolute symlink", []tarEntry{{name: "vogel-0.10.0/examples/boilerplate/link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}}, 1 << 20, "symlink"},
		{"escaping hardlink", []tarEntry{{name: "vogel-0.10.0/examples/boilerplate/hl", typeflag: tar.TypeLink, linkname: "../evil"}}, 1 << 20, "link"},
		{"second top-level dir", []tarEntry{{name: "other/examples/boilerplate/x", body: "x"}}, 1 << 20, "top-level"},
		{"size limit", []tarEntry{{name: "vogel-0.10.0/examples/boilerplate/big", body: strings.Repeat("a", 100)}}, 50, "limit"},
		{"duplicate entry", []tarEntry{{name: "vogel-0.10.0/examples/boilerplate/dup", body: "a"}, {name: "vogel-0.10.0/examples/boilerplate/dup", body: "b"}}, 1 << 20, "duplicate entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "out")
			data := buildTarball(t, append(goodEntries(), tt.extra...))
			err := ExtractBoilerplate(bytes.NewReader(data), dest, tt.limit)
			if err == nil {
				t.Fatal("ExtractBoilerplate() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error %q does not contain %q", err, tt.wantMsg)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "evil")); !os.IsNotExist(err) {
				t.Fatal("traversal entry was written outside the destination")
			}
		})
	}
}

func TestExtractBoilerplateSkipsInnerSymlink(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	entries := append(goodEntries(), tarEntry{name: "vogel-0.10.0/examples/boilerplate/alias", typeflag: tar.TypeSymlink, linkname: "go.mod"})
	if err := ExtractBoilerplate(bytes.NewReader(buildTarball(t, entries)), dest, 1<<20); err != nil {
		t.Fatalf("ExtractBoilerplate() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "alias")); !os.IsNotExist(err) {
		t.Fatal("symlink inside the boilerplate should be skipped, not created")
	}
}

func TestExtractBoilerplateEntryLimit(t *testing.T) {
	// Entries outside the boilerplate are skipped without extracting a byte,
	// so only the entry cap stops a tarball made of millions of them.
	entries := goodEntries()
	for i := len(entries); i <= maxTarEntries; i++ {
		entries = append(entries, tarEntry{name: fmt.Sprintf("vogel-0.10.0/docs/f%d", i), typeflag: tar.TypeDir, mode: 0o755})
	}
	dest := filepath.Join(t.TempDir(), "out")
	err := ExtractBoilerplate(bytes.NewReader(buildTarball(t, entries)), dest, 1<<20)
	if err == nil || !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("error = %v, want entry limit", err)
	}
}

func TestExtractBoilerplateMissing(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	data := buildTarball(t, []tarEntry{{name: "vogel-0.9.0/go.mod", body: "x"}})
	err := ExtractBoilerplate(bytes.NewReader(data), dest, 1<<20)
	if err == nil || !strings.Contains(err.Error(), "examples/boilerplate") {
		t.Fatalf("error = %v, want missing boilerplate", err)
	}
}

func TestFetcherFetch(t *testing.T) {
	data := buildTarball(t, goodEntries())
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path != "/kafeiih/vogel/tar.gz/refs/tags/v0.10.0" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)

	f := &Fetcher{BaseURL: srv.URL, Client: srv.Client()}
	dest := filepath.Join(t.TempDir(), "out")
	if err := f.Fetch(context.Background(), "v0.10.0", dest); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if gotPath != "/kafeiih/vogel/tar.gz/refs/tags/v0.10.0" {
		t.Fatalf("requested %q", gotPath)
	}
	if _, err := os.Stat(filepath.Join(dest, "go.mod")); err != nil {
		t.Fatal(err)
	}

	err := f.Fetch(context.Background(), "v9.9.9", filepath.Join(t.TempDir(), "out"))
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "v9.9.9") {
		t.Fatalf("missing tag error = %v", err)
	}
}

func TestCopyLocal(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module github.com/kafeiih/vogel\n", 0o644)
	bp := filepath.Join(root, "examples", "boilerplate")
	mustWrite(t, filepath.Join(bp, "go.mod"), "module x\n", 0o644)
	mustWrite(t, filepath.Join(bp, "scripts", "run.sh"), "#!/bin/sh\n", 0o755)
	mustWrite(t, filepath.Join(bp, "deploy", ".env"), "SECRET=1\n", 0o600)
	if err := os.Symlink("go.mod", filepath.Join(bp, "alias")); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "out")
	if err := CopyLocal(context.Background(), root, dest); err != nil {
		t.Fatalf("CopyLocal() error = %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dest, "scripts", "run.sh")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("run.sh not copied with exec bit: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "alias")); !os.IsNotExist(err) {
		t.Fatal("symlink was copied")
	}
	if _, err := os.Stat(filepath.Join(dest, "deploy", ".env")); !os.IsNotExist(err) {
		t.Fatal("local .env file was copied into the new project")
	}

	if err := CopyLocal(context.Background(), t.TempDir(), filepath.Join(t.TempDir(), "o")); err == nil || !strings.Contains(err.Error(), "not a vogel checkout") {
		t.Fatalf("CopyLocal() outside a vogel checkout error = %v", err)
	}
	other := t.TempDir()
	mustWrite(t, filepath.Join(other, "go.mod"), "module github.com/kafeiih/vogel\n", 0o644)
	if err := CopyLocal(context.Background(), other, filepath.Join(t.TempDir(), "o")); err == nil || !strings.Contains(err.Error(), "examples/boilerplate") {
		t.Fatalf("CopyLocal() without examples/boilerplate error = %v", err)
	}
}

func mustWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
