package scaffold

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// VogelModule is the module path of the vogel SDK the boilerplate builds on.
const VogelModule = "github.com/kafeiih/vogel"

// binarySniffLen is how many leading bytes are checked for a NUL byte.
const binarySniffLen = 8 << 10

// Options configures Generate.
type Options struct {
	Name   string // project name, ^[a-z][a-z0-9-]*$
	Owner  string // GitHub owner of the new repository
	Module string // Go module path of the new project
	Dir    string // target directory; must not exist or be empty

	// Version is the vogel release to scaffold from and to require. It is
	// required unless From is set; with From it only overrides the require.
	Version string
	// From is the root of a local vogel checkout. When set the boilerplate
	// is copied from disk and go.mod keeps a replace to that checkout.
	From string
	// Fetcher downloads tag tarballs; nil uses the defaults.
	Fetcher *Fetcher
	// Select chooses components once the manifest is known; nil keeps the
	// manifest defaults.
	Select func(*Manifest) (Selection, error)

	SkipTidy   bool // do not run go mod tidy
	SkipChecks bool // do not run the gofmt check
	NoGit      bool // do not run git init and the first commit

	// Log receives progress messages; nil discards them.
	Log io.Writer
}

// Result describes a generated project.
type Result struct {
	Dir      string
	Module   string
	Version  string // vogel version go.mod requires
	Kept     []string
	Removed  []string
	Cascaded []string
	// LocalReplace is the absolute vogel checkout go.mod is replaced with
	// (--from mode only).
	LocalReplace string
	Committed    bool
	// GitNote explains why git was skipped or the commit was not made.
	GitNote string
}

// CommandError is a failed external command. Hint tells the user how to
// finish the step by hand.
type CommandError struct {
	Cmd    string
	Output string
	Err    error
	Hint   string
}

func (e *CommandError) Error() string {
	msg := fmt.Sprintf("%s: %v", e.Cmd, e.Err)
	if out := strings.TrimSpace(e.Output); out != "" {
		msg += "\n" + lastLines(out, maxErrorLines)
	}
	if e.Hint != "" {
		msg += "\n" + e.Hint
	}
	return msg
}

func (e *CommandError) Unwrap() error { return e.Err }

// maxErrorLines bounds how much command output an error carries.
const maxErrorLines = 20

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return fmt.Sprintf("... (%d earlier lines omitted)\n%s", len(lines)-n, strings.Join(lines[len(lines)-n:], "\n"))
}

// Generate creates a new project from the vogel boilerplate. Failures before
// go.mod is edited (including reading go.mod and checking its module path)
// remove what was written to the target directory, or empty it when it
// already existed; later failures leave it for inspection.
func Generate(ctx context.Context, opts Options) (*Result, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, err
	}
	created, err := prepareTarget(dir)
	if err != nil {
		return nil, err
	}

	res, err := opts.render(ctx, dir)
	if err == nil {
		err = opts.checkGoMod(ctx, dir)
	}
	if err != nil {
		cleanupTarget(dir, created)
		return nil, err
	}
	if err := opts.finish(ctx, dir, res); err != nil {
		return res, err
	}
	return res, nil
}

func (o *Options) validate() error {
	if err := ValidateName(o.Name); err != nil {
		return &UsageError{Err: err}
	}
	if err := ValidateOwner(o.Owner); err != nil {
		return &UsageError{Err: err}
	}
	if err := ValidateModulePath(o.Module); err != nil {
		return &UsageError{Err: err}
	}
	if o.Version != "" {
		if err := ValidateVersion(o.Version); err != nil {
			return &UsageError{Err: err}
		}
	}
	if o.From == "" && o.Version == "" {
		return usageErrorf("a vogel version is required unless --from is given")
	}
	if o.Dir == "" {
		return usageErrorf("a target directory is required")
	}
	if o.From != "" {
		abs, err := filepath.Abs(o.From)
		if err != nil {
			return &UsageError{Err: err}
		}
		o.From = abs
	}
	return nil
}

func (o *Options) logf(format string, args ...any) {
	if o.Log != nil {
		_, _ = fmt.Fprintf(o.Log, format+"\n", args...)
	}
}

// prepareTarget creates dir, or accepts it when it exists and is empty. It
// reports whether it created the directory.
func prepareTarget(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return false, err
		}
		return true, nil
	case err != nil:
		return false, &UsageError{Err: fmt.Errorf("target %s: %w", dir, err)}
	case len(entries) > 0:
		return false, usageErrorf("target directory %s already exists and is not empty", dir)
	}
	return false, nil
}

func cleanupTarget(dir string, created bool) {
	if created {
		_ = os.RemoveAll(dir)
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// render fetches the boilerplate and applies every file-level rewrite.
func (o *Options) render(ctx context.Context, dir string) (*Result, error) {
	if o.From != "" {
		o.logf("Copying %s from %s", boilerplateDir, o.From)
		if err := CopyLocal(ctx, o.From, dir); err != nil {
			return nil, err
		}
	} else {
		o.logf("Downloading vogel %s", o.Version)
		if err := o.Fetcher.Fetch(ctx, o.Version, dir); err != nil {
			return nil, err
		}
	}
	m, err := LoadManifest(dir)
	if err != nil {
		return nil, err
	}
	if o.From == "" {
		if err := CheckMinVersion(o.Version, m.MinVogelVersion); err != nil {
			return nil, err
		}
	}

	sel, err := o.selection(m)
	if err != nil {
		return nil, err
	}
	removed, err := removePaths(dir, m, sel)
	if err != nil {
		return nil, err
	}
	values := Derive(o.Name, o.Owner, o.Module)
	if err := applyReplacements(dir, m, values, removed); err != nil {
		return nil, err
	}
	if err := rewriteFiles(dir, m, sel, o.Module); err != nil {
		return nil, err
	}

	res := &Result{
		Dir:      dir,
		Module:   o.Module,
		Kept:     sel.Kept(),
		Removed:  sel.Removed(),
		Cascaded: sel.Cascaded,
	}
	if o.From != "" {
		res.LocalReplace = o.From
	}
	return res, nil
}

func (o *Options) selection(m *Manifest) (Selection, error) {
	if o.Select == nil {
		return Select(m, nil, nil, nil)
	}
	return o.Select(m)
}

// removePaths deletes the paths of deselected components and alwaysRemove.
// Every listed path must exist, so a renamed directory fails loudly.
func removePaths(dir string, m *Manifest, sel Selection) ([]string, error) {
	var removed []string
	check := func(p string) error {
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			return fmt.Errorf("template manifest lists %s, which is not in the boilerplate: %w", p, err)
		}
		return nil
	}
	for _, c := range m.Components {
		for _, p := range c.Paths {
			if err := check(p); err != nil {
				return nil, err
			}
			if !sel.Selected[c.Name] {
				removed = append(removed, p)
			}
		}
	}
	for _, p := range m.AlwaysRemove {
		if err := check(p); err != nil {
			return nil, err
		}
		removed = append(removed, p)
	}
	for _, p := range removed {
		if err := os.RemoveAll(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			return nil, err
		}
	}
	return removed, nil
}

func underAny(file string, prefixes []string) bool {
	for _, p := range prefixes {
		if file == p || strings.HasPrefix(file, p+"/") {
			return true
		}
	}
	return false
}

// applyReplacements runs the manifest's literal replacements. An entry whose
// file was removed with a deselected component is skipped; any other missing
// file or a missing `from` is template drift and fails.
func applyReplacements(dir string, m *Manifest, values TemplateValues, removed []string) error {
	data := values.asMap()
	for _, r := range m.Replacements {
		file := filepath.Join(dir, filepath.FromSlash(r.Path))
		content, err := os.ReadFile(file) //nolint:gosec // r.Path is a validated relative path inside the generated project
		if errors.Is(err, fs.ErrNotExist) && underAny(r.Path, removed) {
			continue
		}
		if err != nil {
			return fmt.Errorf("replacement in %s: %w", r.Path, err)
		}
		if !bytes.Contains(content, []byte(r.From)) {
			return fmt.Errorf("replacement %q not found in %s (the template changed; update its manifest)", r.From, r.Path)
		}
		tmpl, err := parseReplacementTemplate(r.To)
		if err != nil {
			return fmt.Errorf("replacement in %s: %w", r.Path, err)
		}
		var to strings.Builder
		if err := tmpl.Execute(&to, data); err != nil {
			return fmt.Errorf("replacement in %s: %w", r.Path, err)
		}
		if err := writeKeepingMode(file, bytes.ReplaceAll(content, []byte(r.From), []byte(to.String()))); err != nil {
			return err
		}
	}
	return nil
}

// rewriteFiles strips markers from every text file and rewrites the module
// path in Go, go.mod and Markdown files.
func rewriteFiles(dir string, m *Manifest, sel Selection, module string) error {
	oldModule, newModule := []byte(m.ModulePath), []byte(module)
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		content, err := os.ReadFile(p) //nolint:gosec // p comes from walking the generated project directory
		if err != nil {
			return err
		}
		if isBinary(content) {
			return nil
		}
		out, err := StripMarkers(content, sel.Selected)
		if err != nil {
			var me *MarkerError
			if errors.As(err, &me) {
				me.File = rel
			}
			return err
		}
		if base := path.Base(rel); base == "go.mod" || strings.HasSuffix(base, ".go") || strings.HasSuffix(base, ".md") {
			out = bytes.ReplaceAll(out, oldModule, newModule)
		}
		if bytes.Equal(out, content) {
			return nil
		}
		return writeKeepingMode(p, out)
	})
}

func isBinary(content []byte) bool {
	return bytes.IndexByte(content[:min(len(content), binarySniffLen)], 0) >= 0
}

// writeKeepingMode overwrites an existing file; os.WriteFile keeps the mode
// of a file that already exists.
func writeKeepingMode(file string, content []byte) error {
	return os.WriteFile(file, content, 0o644) //nolint:gosec // the file already exists inside the generated project; its mode is preserved
}

// checkGoMod checks that the rewritten go.mod declares the new module, which
// fails when the manifest modulePath does not match the boilerplate go.mod.
func (o *Options) checkGoMod(ctx context.Context, dir string) error {
	mod, err := readGoMod(ctx, dir)
	if err != nil {
		return err
	}
	if mod.Module.Path != o.Module {
		return fmt.Errorf("go.mod declares module %q after the rewrite, want %q (the manifest modulePath does not match the boilerplate go.mod)", mod.Module.Path, o.Module)
	}
	return nil
}

// finish edits go.mod, tidies, checks formatting and creates the git repo.
func (o *Options) finish(ctx context.Context, dir string, res *Result) error {
	args := []string{"mod", "edit"}
	if o.From == "" {
		args = append(args, "-dropreplace="+VogelModule)
	} else {
		args = append(args, "-replace="+VogelModule+"="+o.From)
	}
	if o.Version != "" {
		args = append(args, "-require="+VogelModule+"@"+o.Version)
	}
	if _, err := runCmd(ctx, dir, "go", args...); err != nil {
		return err
	}
	res.Version = o.Version
	if res.Version == "" {
		mod, err := readGoMod(ctx, dir)
		if err != nil {
			return err
		}
		res.Version = mod.require(VogelModule)
	}

	if !o.SkipTidy {
		o.logf("Running go mod tidy")
		if _, err := runCmd(ctx, dir, "go", "mod", "tidy"); err != nil {
			var ce *CommandError
			if errors.As(err, &ce) {
				ce.Hint = fmt.Sprintf("go mod tidy failed; the project was left in %s. Fix the problem and run `go mod tidy` there.", dir)
			}
			return err
		}
	}
	if !o.SkipChecks {
		if err := checkGofmt(ctx, dir); err != nil {
			return err
		}
	}
	if o.NoGit {
		res.GitNote = "skipped (--no-git)"
		return nil
	}
	return o.initGit(ctx, dir, res)
}

type goMod struct {
	Module struct {
		Path string
	}
	Require []struct {
		Path    string
		Version string
	}
}

func (m *goMod) require(module string) string {
	for _, r := range m.Require {
		if r.Path == module {
			return r.Version
		}
	}
	return ""
}

func readGoMod(ctx context.Context, dir string) (*goMod, error) {
	out, err := runCmd(ctx, dir, "go", "mod", "edit", "-json")
	if err != nil {
		return nil, err
	}
	var m goMod
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		return nil, fmt.Errorf("parse go.mod: %w", err)
	}
	return &m, nil
}

func checkGofmt(ctx context.Context, dir string) error {
	gofmt, err := gofmtPath(ctx)
	if err != nil {
		return err
	}
	out, err := runCmd(ctx, dir, gofmt, "-l", ".")
	if err != nil {
		return err
	}
	if files := strings.TrimSpace(out); files != "" {
		return fmt.Errorf("generated Go files are not gofmt-formatted (a marker block is probably misplaced in the template):\n%s", files)
	}
	return nil
}

func gofmtPath(ctx context.Context) (string, error) {
	if out, err := runCmd(ctx, "", "go", "env", "GOROOT"); err == nil {
		p := filepath.Join(strings.TrimSpace(out), "bin", "gofmt")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	p, err := exec.LookPath("gofmt")
	if err != nil {
		return "", errors.New("gofmt not found; install Go or pass --skip-checks")
	}
	return p, nil
}

func (o *Options) initGit(ctx context.Context, dir string, res *Result) error {
	if _, err := runCmd(ctx, dir, "git", "init", "-q", "-b", "main"); err != nil {
		return err
	}
	for _, key := range []string{"user.name", "user.email"} {
		out, err := runCmd(ctx, dir, "git", "config", key)
		if configured := err == nil && strings.TrimSpace(out) != ""; !configured {
			res.GitNote = fmt.Sprintf("git %s is not configured (both user.name and user.email are needed), so the initial commit was skipped; configure them and run `git add -A && git commit -m %q`", key, commitMessage(o.Name, res.Version))
			return nil
		}
	}
	if _, err := runCmd(ctx, dir, "git", "add", "-A"); err != nil {
		return err
	}
	if _, err := runCmd(ctx, dir, "git", "commit", "-q", "-m", commitMessage(o.Name, res.Version)); err != nil {
		return err
	}
	res.Committed = true
	return nil
}

func commitMessage(name, version string) string {
	return fmt.Sprintf("chore: scaffold %s from vogel %s", name, version)
}

// runCmd runs an external command in dir and returns its standard output.
// Go commands run with GOWORK=off so an enclosing workspace cannot leak in.
func runCmd(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed tools (go, gofmt, git) with arguments built by this package
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), &CommandError{Cmd: name + " " + strings.Join(args, " "), Output: stderr.String(), Err: err}
	}
	return stdout.String(), nil
}
