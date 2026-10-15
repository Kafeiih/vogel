package scaffold

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// DefaultTarballBaseURL serves GitHub tag tarballs.
	DefaultTarballBaseURL = "https://codeload.github.com"
	// DefaultMaxExtractBytes caps the total size of extracted files.
	DefaultMaxExtractBytes int64 = 64 << 20
	// maxDownloadBytes caps the compressed download.
	maxDownloadBytes   int64 = 256 << 20
	defaultHTTPTimeout       = 2 * time.Minute

	boilerplateDir = "examples/boilerplate"
)

// Fetcher downloads the boilerplate of a vogel tag.
type Fetcher struct {
	// BaseURL defaults to DefaultTarballBaseURL; tests point it at an
	// httptest server.
	BaseURL string
	// Client defaults to an http.Client with a two-minute timeout.
	Client *http.Client
	// MaxBytes caps the extracted size; it defaults to DefaultMaxExtractBytes.
	MaxBytes int64
}

// Fetch downloads the tarball of version and extracts its
// examples/boilerplate into dest.
func (f *Fetcher) Fetch(ctx context.Context, version, dest string) error {
	if err := ValidateVersion(version); err != nil {
		return err
	}
	base := DefaultTarballBaseURL
	client := &http.Client{Timeout: defaultHTTPTimeout}
	limit := DefaultMaxExtractBytes
	if f != nil {
		if f.BaseURL != "" {
			base = f.BaseURL
		}
		if f.Client != nil {
			client = f.Client
		}
		if f.MaxBytes > 0 {
			limit = f.MaxBytes
		}
	}
	url := strings.TrimSuffix(base, "/") + "/kafeiih/vogel/tar.gz/refs/tags/" + version
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download vogel %s: %w", version, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download vogel %s from %s: unexpected status %s", version, url, resp.Status)
	}
	if err := ExtractBoilerplate(io.LimitReader(resp.Body, maxDownloadBytes), dest, limit); err != nil {
		return fmt.Errorf("vogel %s: %w", version, err)
	}
	return nil
}

// ExtractBoilerplate reads a gzip-compressed tar of the vogel repository (a
// single top-level directory, as GitHub serves it) and writes the entries
// under <top>/examples/boilerplate/ into dest. Unsafe paths and links that
// escape the boilerplate are rejected; other symlinks and special files are
// skipped. maxBytes caps the total size of the extracted files.
func ExtractBoilerplate(r io.Reader, dest string, maxBytes int64) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("read tarball: %w", err)
	}
	defer func() { _ = gz.Close() }()

	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	tr := tar.NewReader(gz)
	var (
		top     string
		total   int64
		written bool
	)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tarball: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if !isSafeArchivePath(name) {
			return fmt.Errorf("unsafe path in tarball: %q", hdr.Name)
		}
		first, _, _ := strings.Cut(name, "/")
		if top == "" {
			top = first
		} else if first != top {
			return fmt.Errorf("tarball has more than one top-level directory (%q and %q)", top, first)
		}
		rel, ok := strings.CutPrefix(name, top+"/"+boilerplateDir)
		if !ok || (rel != "" && rel[0] != '/') {
			continue
		}
		rel = strings.TrimPrefix(rel, "/")

		switch hdr.Typeflag {
		case tar.TypeDir:
			if rel != "" {
				if err := root.MkdirAll(rel, 0o750); err != nil {
					return err
				}
			}
			written = true
		case tar.TypeReg:
			total += hdr.Size
			if hdr.Size < 0 || total > maxBytes {
				return fmt.Errorf("boilerplate exceeds the %d byte extraction limit", maxBytes)
			}
			if err := extractFile(root, rel, tr, hdr); err != nil {
				return err
			}
			written = true
		case tar.TypeSymlink, tar.TypeLink:
			escaping := escapes(rel, hdr.Linkname)
			if hdr.Typeflag == tar.TypeLink {
				// Hard link targets are archive paths, not relative to the link.
				escaping = !isSafeArchivePath(hdr.Linkname) ||
					!strings.HasPrefix(hdr.Linkname, top+"/"+boilerplateDir+"/")
			}
			if escaping {
				return fmt.Errorf("%s %q points outside the boilerplate (%q)", linkKind(hdr.Typeflag), hdr.Name, hdr.Linkname)
			}
			// Links inside the boilerplate are skipped, never followed.
		default:
			// Devices, FIFOs and other special entries are skipped.
		}
	}
	if !written {
		return fmt.Errorf("tarball does not contain %s", boilerplateDir)
	}
	return nil
}

func extractFile(root *os.Root, rel string, r io.Reader, hdr *tar.Header) error {
	if dir := path.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode(hdr.Mode&0o111 != 0))
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("tarball has a duplicate entry for %s", rel)
	}
	if err != nil {
		return err
	}
	if _, err := io.CopyN(f, r, hdr.Size); err != nil {
		_ = f.Close()
		return fmt.Errorf("extract %s: %w", rel, err)
	}
	return f.Close()
}

// fileMode normalizes a file mode to 0644, or 0755 when the source had any
// execute bit.
func fileMode(executable bool) fs.FileMode {
	if executable {
		return 0o755
	}
	return 0o644
}

func linkKind(t byte) string {
	if t == tar.TypeSymlink {
		return "symlink"
	}
	return "hard link"
}

func isSafeArchivePath(name string) bool {
	if name == "" || strings.Contains(name, `\`) || path.IsAbs(name) || filepath.IsAbs(name) {
		return false
	}
	for _, elem := range strings.Split(name, "/") {
		if elem == ".." {
			return false
		}
	}
	return true
}

// escapes reports whether a link at rel (relative to the boilerplate root)
// with the given target resolves outside the boilerplate.
func escapes(rel, target string) bool {
	if target == "" || path.IsAbs(target) || filepath.IsAbs(target) || strings.Contains(target, `\`) {
		return true
	}
	resolved := path.Join(path.Dir(rel), target)
	return resolved == ".." || strings.HasPrefix(resolved, "../")
}

var vogelModuleLineRe = regexp.MustCompile(`(?m)^module\s+github\.com/kafeiih/vogel\s*$`)

// CopyLocal copies <vogelRoot>/examples/boilerplate into dest. When the
// checkout is a git repository only files git would publish are copied
// (tracked plus untracked-but-not-ignored), so build output never reaches the
// new project; otherwise the directory is walked. .env files, symlinks and
// special files are never copied.
func CopyLocal(ctx context.Context, vogelRoot, dest string) error {
	gomod, err := os.ReadFile(filepath.Join(vogelRoot, "go.mod")) //nolint:gosec // vogelRoot is the checkout the user passed with --from
	if err != nil || !vogelModuleLineRe.Match(gomod) {
		return fmt.Errorf("%s is not a vogel checkout: its go.mod does not declare module github.com/kafeiih/vogel", vogelRoot)
	}
	src := filepath.Join(vogelRoot, filepath.FromSlash(boilerplateDir))
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a vogel checkout: %s not found", vogelRoot, boilerplateDir)
	}
	files, err := gitListFiles(ctx, src)
	if err != nil {
		files, err = walkFiles(src)
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, rel := range files {
		if isEnvFile(path.Base(rel)) {
			continue
		}
		if err := copyRegular(root, src, rel); err != nil {
			return err
		}
	}
	return nil
}

func gitListFiles(ctx context.Context, src string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".")
	cmd.Dir = src
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) > 0 {
			files = append(files, string(f))
		}
	}
	return files, nil
}

func walkFiles(src string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, err
}

func isEnvFile(name string) bool {
	return name == ".env" || strings.HasPrefix(name, ".env.")
}

// copyRegular copies src/rel into root when it is a regular file. Missing
// files (deleted but still in the git index) and non-regular files are
// skipped.
func copyRegular(root *os.Root, src, rel string) error {
	if !isSafeArchivePath(rel) {
		return fmt.Errorf("unsafe path %q", rel)
	}
	p := filepath.Join(src, filepath.FromSlash(rel))
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	in, err := os.Open(p) //nolint:gosec // p is a regular file inside the boilerplate directory chosen with --from
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if dir := path.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	out, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode(fi.Mode()&0o111 != 0))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
