package main

import (
	"io/fs"
	"testing"

	auditmigrations "github.com/kafeiih/vogel/audit/migrations"
	// vogel:component bpms begin

	actionmigrations "github.com/kafeiih/vogel/action/migrations"
	documentmigrations "github.com/kafeiih/vogel/document/migrations"
	workflowmigrations "github.com/kafeiih/vogel/workflow/migrations"

	bpmsmigrations "github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/bpms/migrations"
	// vogel:component bpms end
)

func TestMigrationSets(t *testing.T) {
	sets := migrationSets()

	want := []struct {
		name      string
		tableName string
	}{
		{name: "app", tableName: ""},
		{name: "audit", tableName: auditmigrations.DefaultTableName},
		// vogel:component bpms begin
		{name: "workflow", tableName: workflowmigrations.DefaultTableName},
		{name: "document", tableName: documentmigrations.DefaultTableName},
		{name: "action", tableName: actionmigrations.DefaultTableName},
		{name: "bpms", tableName: bpmsmigrations.TableName},
		// vogel:component bpms end
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
