package scaffold

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"
)

// ManifestPath is the template manifest location, relative to the
// boilerplate root.
const ManifestPath = ".vogel/template.json"

// Manifest describes how to turn the boilerplate into a new project.
type Manifest struct {
	ModulePath      string        `json:"modulePath"`
	MinVogelVersion string        `json:"minVogelVersion"`
	Components      []Component   `json:"components"`
	AlwaysRemove    []string      `json:"alwaysRemove"`
	Replacements    []Replacement `json:"replacements"`
}

// Component is an optional part of the boilerplate.
type Component struct {
	Name     string   `json:"name"`
	Prompt   string   `json:"prompt"`
	Default  bool     `json:"default"`
	Requires []string `json:"requires"`
	Paths    []string `json:"paths"`
}

// Replacement is a literal search and replace in a single file. To is a
// text/template rendered with TemplateValues.
type Replacement struct {
	Path string `json:"path"`
	From string `json:"from"`
	To   string `json:"to"`
}

// LoadManifest reads ManifestPath from the boilerplate directory dir.
func LoadManifest(dir string) (*Manifest, error) {
	f, err := os.Open(filepath.Join(dir, filepath.FromSlash(ManifestPath))) //nolint:gosec // dir is the boilerplate copy this package just wrote
	if err != nil {
		return nil, fmt.Errorf("template manifest: %w", err)
	}
	defer func() { _ = f.Close() }()
	return ParseManifest(f)
}

// ParseManifest decodes and validates a manifest. Unknown fields are
// rejected so that drift between the CLI and the template fails loudly.
func ParseManifest(r io.Reader) (*Manifest, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("template manifest: %w", err)
	}
	if dec.More() {
		return nil, errors.New("template manifest: trailing data after the JSON object")
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("template manifest: %w", err)
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	if m.ModulePath == "" {
		return errors.New("modulePath is required")
	}
	if m.MinVogelVersion != "" {
		if err := ValidateVersion(m.MinVogelVersion); err != nil {
			return fmt.Errorf("minVogelVersion: %w", err)
		}
	}
	seen := make(map[string]bool, len(m.Components))
	for _, c := range m.Components {
		if !componentRe.MatchString(c.Name) {
			return fmt.Errorf("invalid component name %q: want ^[a-z]+$", c.Name)
		}
		if seen[c.Name] {
			return fmt.Errorf("duplicate component %q", c.Name)
		}
		for _, req := range c.Requires {
			if !seen[req] {
				return fmt.Errorf("component %q requires %q, which must be declared before it", c.Name, req)
			}
		}
		seen[c.Name] = true
		for _, p := range c.Paths {
			if err := checkRelPath(p); err != nil {
				return fmt.Errorf("component %q: %w", c.Name, err)
			}
		}
	}
	for _, p := range m.AlwaysRemove {
		if err := checkRelPath(p); err != nil {
			return fmt.Errorf("alwaysRemove: %w", err)
		}
	}
	for i, r := range m.Replacements {
		if err := checkRelPath(r.Path); err != nil {
			return fmt.Errorf("replacements[%d]: %w", i, err)
		}
		if r.From == "" {
			return fmt.Errorf("replacements[%d]: from must not be empty", i)
		}
		if _, err := parseReplacementTemplate(r.To); err != nil {
			return fmt.Errorf("replacements[%d]: %w", i, err)
		}
	}
	return nil
}

func parseReplacementTemplate(to string) (*template.Template, error) {
	t, err := template.New("to").Option("missingkey=error").Parse(to)
	if err != nil {
		return nil, fmt.Errorf("invalid template: %w", err)
	}
	return t, nil
}

// checkRelPath accepts clean, relative, forward-slash paths that stay inside
// the project.
func checkRelPath(p string) error {
	if p == "" || strings.Contains(p, `\`) || path.IsAbs(p) || path.Clean(p) != p || p == "." ||
		p == ".." || strings.HasPrefix(p, "../") || filepath.IsAbs(p) {
		return fmt.Errorf("invalid path %q: want a clean relative path with forward slashes", p)
	}
	return nil
}

// UsageError marks an error caused by the user's input (flags, names).
type UsageError struct {
	Err error
}

func (e *UsageError) Error() string { return e.Err.Error() }

func (e *UsageError) Unwrap() error { return e.Err }

func usageErrorf(format string, args ...any) error {
	return &UsageError{Err: fmt.Errorf(format, args...)}
}

// Selection is the outcome of choosing components.
type Selection struct {
	// Selected maps every manifest component to whether it is kept.
	Selected map[string]bool
	// Cascaded lists components deselected because a requirement was not
	// selected.
	Cascaded []string

	order []string
}

// Kept returns the selected components in manifest order.
func (s Selection) Kept() []string { return s.filter(true) }

// Removed returns the deselected components in manifest order.
func (s Selection) Removed() []string { return s.filter(false) }

func (s Selection) filter(keep bool) []string {
	var out []string
	for _, name := range s.order {
		if s.Selected[name] == keep {
			out = append(out, name)
		}
	}
	return out
}

// AskFunc asks the user whether to keep a component.
type AskFunc func(c Component) (bool, error)

// Select chooses components in manifest order. Names in with and without are
// forced on or off; the rest come from ask, or from their defaults when ask
// is nil. A component whose requirement is not selected is deselected without
// asking and reported in Selection.Cascaded.
func Select(m *Manifest, with, without []string, ask AskFunc) (Selection, error) {
	forced := make(map[string]bool, len(with)+len(without))
	for _, n := range with {
		if !m.hasComponent(n) {
			return Selection{}, usageErrorf("--with: unknown component %q (known: %s)", n, strings.Join(m.componentNames(), ", "))
		}
		forced[n] = true
	}
	for _, n := range without {
		if !m.hasComponent(n) {
			return Selection{}, usageErrorf("--without: unknown component %q (known: %s)", n, strings.Join(m.componentNames(), ", "))
		}
		if forced[n] {
			return Selection{}, usageErrorf("component %q is given to both --with and --without", n)
		}
		forced[n] = false
	}

	sel := Selection{Selected: make(map[string]bool, len(m.Components)), order: m.componentNames()}
	for _, c := range m.Components {
		missing := ""
		for _, req := range c.Requires {
			if !sel.Selected[req] {
				missing = req
				break
			}
		}
		if missing != "" {
			if forced[c.Name] {
				return Selection{}, usageErrorf("--with %s requires %s, which is not selected", c.Name, missing)
			}
			sel.Selected[c.Name] = false
			sel.Cascaded = append(sel.Cascaded, c.Name)
			continue
		}
		keep, isForced := forced[c.Name]
		switch {
		case isForced:
		case ask != nil:
			var err error
			if keep, err = ask(c); err != nil {
				return Selection{}, err
			}
		default:
			keep = c.Default
		}
		sel.Selected[c.Name] = keep
	}
	return sel, nil
}

func (m *Manifest) hasComponent(name string) bool {
	for _, c := range m.Components {
		if c.Name == name {
			return true
		}
	}
	return false
}

func (m *Manifest) componentNames() []string {
	names := make([]string, 0, len(m.Components))
	for _, c := range m.Components {
		names = append(names, c.Name)
	}
	return names
}
