package scaffold

import (
	"errors"
	"strings"
	"testing"
)

const validManifest = `{
  "modulePath": "github.com/kafeiih/vogel/examples/boilerplate",
  "minVogelVersion": "v0.10.0",
  "components": [
    {"name": "worker", "prompt": "Worker?", "default": true, "requires": [], "paths": ["cmd/worker"]},
    {"name": "notification", "prompt": "Notifications?", "default": true, "requires": ["worker"], "paths": []},
    {"name": "cerbos", "prompt": "Cerbos?", "default": true, "requires": [], "paths": ["cerbos"]},
    {"name": "storage", "prompt": "Storage?", "default": false, "requires": [], "paths": []}
  ],
  "alwaysRemove": [".vogel"],
  "replacements": [
    {"path": "deploy/docker-compose.yaml", "from": "ghcr.io/kafeiih/vogel-boilerplate", "to": "{{.ImageBase}}"}
  ]
}`

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest(strings.NewReader(validManifest))
	if err != nil {
		t.Fatalf("ParseManifest() error = %v", err)
	}
	if m.ModulePath != "github.com/kafeiih/vogel/examples/boilerplate" || m.MinVogelVersion != "v0.10.0" {
		t.Fatalf("unexpected header fields: %+v", m)
	}
	if len(m.Components) != 4 || m.Components[1].Requires[0] != "worker" || m.Components[3].Default {
		t.Fatalf("unexpected components: %+v", m.Components)
	}
	if len(m.Replacements) != 1 || m.Replacements[0].To != "{{.ImageBase}}" {
		t.Fatalf("unexpected replacements: %+v", m.Replacements)
	}
}

func TestParseManifestRejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(string) string
		wantMsg string
	}{
		{"unknown field", func(s string) string {
			return strings.Replace(s, `"alwaysRemove"`, `"surprise": 1, "alwaysRemove"`, 1)
		}, "surprise"},
		{"unknown component field", func(s string) string {
			return strings.Replace(s, `"name": "storage",`, `"name": "storage", "extra": true,`, 1)
		}, "extra"},
		{"trailing data", func(s string) string { return s + "{}" }, "trailing"},
		{"missing module path", func(s string) string {
			return strings.Replace(s, `"github.com/kafeiih/vogel/examples/boilerplate"`, `""`, 1)
		}, "modulePath"},
		{"invalid min version", func(s string) string { return strings.Replace(s, `"v0.10.0"`, `"0.10"`, 1) }, "minVogelVersion"},
		{"duplicate component", func(s string) string {
			return strings.Replace(s, `"name": "storage"`, `"name": "cerbos"`, 1)
		}, "duplicate"},
		{"invalid component name", func(s string) string {
			return strings.Replace(s, `"name": "storage"`, `"name": "Storage2"`, 1)
		}, "invalid component name"},
		{"unknown requirement", func(s string) string { return strings.Replace(s, `["worker"]`, `["billing"]`, 1) }, "billing"},
		{"requirement declared later", func(s string) string {
			return strings.Replace(s, `"requires": [], "paths": ["cerbos"]`, `"requires": ["storage"], "paths": ["cerbos"]`, 1)
		}, "declared before"},
		{"parent path", func(s string) string { return strings.Replace(s, `["cmd/worker"]`, `["../cmd"]`, 1) }, "invalid path"},
		{"absolute path", func(s string) string { return strings.Replace(s, `["cerbos"]`, `["/etc"]`, 1) }, "invalid path"},
		{"backslash path", func(s string) string { return strings.Replace(s, `[".vogel"]`, `["a\\b"]`, 1) }, "invalid path"},
		{"empty replacement from", func(s string) string {
			return strings.Replace(s, `"ghcr.io/kafeiih/vogel-boilerplate"`, `""`, 1)
		}, "from"},
		{"bad replacement template", func(s string) string { return strings.Replace(s, `{{.ImageBase}}`, `{{.ImageBase`, 1) }, "template"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseManifest(strings.NewReader(tt.mutate(validManifest)))
			if err == nil {
				t.Fatal("ParseManifest() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error %q does not contain %q", err, tt.wantMsg)
			}
		})
	}
}

func mustManifest(t *testing.T) *Manifest {
	t.Helper()
	m, err := ParseManifest(strings.NewReader(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSelectDefaultsAndFlags(t *testing.T) {
	m := mustManifest(t)
	tests := []struct {
		name         string
		with         []string
		without      []string
		wantSelected []string
		wantCascaded []string
	}{
		{"defaults", nil, nil, []string{"worker", "notification", "cerbos"}, nil},
		{"with overrides default", []string{"storage"}, nil, []string{"worker", "notification", "cerbos", "storage"}, nil},
		{"without overrides default", nil, []string{"cerbos"}, []string{"worker", "notification"}, nil},
		{"requires cascade", nil, []string{"worker"}, []string{"cerbos"}, []string{"notification"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, err := Select(m, tt.with, tt.without, nil)
			if err != nil {
				t.Fatalf("Select() error = %v", err)
			}
			if got := strings.Join(sel.Kept(), ","); got != strings.Join(tt.wantSelected, ",") {
				t.Fatalf("Kept() = %s, want %s", got, strings.Join(tt.wantSelected, ","))
			}
			if got := strings.Join(sel.Cascaded, ","); got != strings.Join(tt.wantCascaded, ",") {
				t.Fatalf("Cascaded = %s, want %s", got, strings.Join(tt.wantCascaded, ","))
			}
		})
	}
}

func TestSelectUsageErrors(t *testing.T) {
	m := mustManifest(t)
	tests := []struct {
		name          string
		with, without []string
		wantMsg       string
	}{
		{"unknown with", []string{"billing"}, nil, `unknown component "billing"`},
		{"unknown without", nil, []string{"billing"}, `unknown component "billing"`},
		{"with and without", []string{"cerbos"}, []string{"cerbos"}, "both --with and --without"},
		{"with whose requirement is excluded", []string{"notification"}, []string{"worker"}, "requires"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Select(m, tt.with, tt.without, nil)
			var ue *UsageError
			if !errors.As(err, &ue) {
				t.Fatalf("Select() error = %v, want *UsageError", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error %q does not contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestSelectInteractive(t *testing.T) {
	m := mustManifest(t)
	var asked []string
	answers := map[string]bool{"worker": false, "cerbos": true, "storage": true}
	ask := func(c Component) (bool, error) {
		asked = append(asked, c.Name)
		return answers[c.Name], nil
	}
	sel, err := Select(m, nil, []string{"cerbos"}, ask)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	// worker is answered no, so notification is never asked; cerbos is fixed by a flag.
	if got := strings.Join(asked, ","); got != "worker,storage" {
		t.Fatalf("asked = %s, want worker,storage", got)
	}
	if got := strings.Join(sel.Kept(), ","); got != "storage" {
		t.Fatalf("Kept() = %s, want storage", got)
	}
	if got := strings.Join(sel.Removed(), ","); got != "worker,notification,cerbos" {
		t.Fatalf("Removed() = %s", got)
	}
	if got := strings.Join(sel.Cascaded, ","); got != "notification" {
		t.Fatalf("Cascaded = %s, want notification", got)
	}
}

// TestLoadManifestMissingNamesMinimumVersion pins the message for a vogel
// version whose boilerplate predates create-vogel-app (no manifest).
func TestLoadManifestMissingNamesMinimumVersion(t *testing.T) {
	_, err := LoadManifest(t.TempDir())
	if err == nil {
		t.Fatal("LoadManifest(empty dir) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "predates create-vogel-app") {
		t.Fatalf("error %q does not say the template predates create-vogel-app", err)
	}
}
