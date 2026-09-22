package main

import (
	"io/fs"
	"testing"

	auditmigrations "github.com/kafeiih/vogel/audit/migrations"
)

func TestMigrationSets(t *testing.T) {
	sets := migrationSets()

	want := []struct {
		name      string
		tableName string
	}{
		{name: "app", tableName: ""},
		{name: "audit", tableName: auditmigrations.DefaultTableName},
	}
	if len(sets) != len(want) {
		t.Fatalf("migrationSets() returned %d sets, want %d", len(sets), len(want))
	}
	for i, w := range want {
		got := sets[i]
		if got.name != w.name {
			t.Errorf("set %d: name = %q, want %q", i, got.name, w.name)
		}
		if got.opts.TableName != w.tableName {
			t.Errorf("set %q: TableName = %q, want %q", got.name, got.opts.TableName, w.tableName)
		}
		files, err := fs.Glob(got.fsys, "*.sql")
		if err != nil {
			t.Fatalf("set %q: glob: %v", got.name, err)
		}
		if len(files) == 0 {
			t.Errorf("set %q: no .sql migrations at the FS root", got.name)
		}
	}
}
