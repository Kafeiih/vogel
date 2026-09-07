package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"spaces to underscores", "add users table", "add_users_table"},
		{"lowercase", "AddUsers", "addusers"},
		{"collapse separators", "add--users  table", "add_users_table"},
		{"trim edges", "  add_users  ", "add_users"},
		{"strip punctuation", "add users (v2)!", "add_users_v2"},
		{"keep digits", "create 001 table", "create_001_table"},
		{"empty", "", ""},
		{"only separators", "   --  ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := slugify(tt.input); got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNextSequence(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "001_first.sql")
	writeFile(t, dir, "002_second.sql")
	writeFile(t, dir, "not_a_migration.sql") // non-numeric prefix ignored
	writeFile(t, dir, "README.md")           // non-sql ignored

	got, err := nextSequence(dir)
	if err != nil {
		t.Fatalf("nextSequence: unexpected error: %v", err)
	}
	if got != 3 {
		t.Errorf("nextSequence = %d, want 3", got)
	}
}

func TestNextSequence_EmptyDir(t *testing.T) {
	got, err := nextSequence(t.TempDir())
	if err != nil {
		t.Fatalf("nextSequence: unexpected error: %v", err)
	}
	if got != 1 {
		t.Errorf("nextSequence on empty dir = %d, want 1", got)
	}
}

func TestCreate(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "001_create_audit_log.sql")

	path, err := Create(dir, "Add Users Table")
	if err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	wantName := "002_add_users_table.sql"
	if filepath.Base(path) != wantName {
		t.Errorf("Create produced %q, want basename %q", path, wantName)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading created file: %v", err)
	}
	for _, marker := range []string{"-- +goose Up", "-- +goose Down", "-- +goose StatementBegin", "-- +goose StatementEnd"} {
		if !strings.Contains(string(content), marker) {
			t.Errorf("created migration missing %q marker", marker)
		}
	}
}

func TestCreate_EmptyName(t *testing.T) {
	if _, err := Create(t.TempDir(), "  --  "); err == nil {
		t.Error("Create with effectively empty name: expected error, got nil")
	}
}

func writeFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("-- test\n"), 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", name, err)
	}
}
