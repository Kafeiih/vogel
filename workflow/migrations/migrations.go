// Package migrations embeds the SQL migration(s) that create the
// workflow_case and workflow_event tables this library owns.
//
// 005_workflow_event_append_only.sql makes workflow_event append-only:
// triggers reject UPDATE, DELETE and TRUNCATE on it with SQLSTATE 23001
// (restrict_violation) and a message starting with "workflow_event is
// append-only", and the case_id foreign key is ON DELETE RESTRICT, so
// deleting a case with events fails (23503). The guarantee has limits: the
// table owner or a superuser can disable the triggers. Any retention or
// purge must be an explicit owner-run procedure that disables the triggers,
// deletes and re-enables them inside one transaction; the recipe is in the
// header of 005_workflow_event_append_only.sql. Integration tests that clean
// these tables between cases must not TRUNCATE or DELETE them: use a fresh
// database per test or roll back a transaction instead.
//
// Consumers pass FS() to vogel/migrate.Up (or UpTo/UpByOne/Status) alongside
// their own application migrations' fs.FS. Because this library's version
// numbering starts at 001 independently of each application's own migration
// numbering (and independently of vogel/audit/migrations' own numbering),
// this set MUST run against its own goose version table — see
// DefaultTableName, migrate.Options.TableName, and the "Running library
// migrations alongside application migrations" section of the top-level
// README.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS returns the embedded workflow migration(s) as an fs.FS, ready to pass
// to vogel/migrate.Up.
func FS() fs.FS {
	return files
}

// DefaultTableName is the suggested goose version-table name for this
// library's migrations, kept separate from an application's own
// "goose_db_version" table (and from vogel/audit/migrations'
// "vogel_db_version") so independently-numbered migration sets never
// collide. It is only a suggestion — migrate.Options.TableName is a
// caller-supplied option, not a hardcoded constant in vogel/migrate itself —
// but every consumer should use the same value so operators find one
// well-known table name across systems.
const DefaultTableName = "workflow_db_version"
