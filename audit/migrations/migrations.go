// Package migrations embeds the SQL migration(s) that create the audit_log
// table this library owns and declare it append-only.
//
// 001_create_audit_log.sql creates the table. 002_audit_log_append_only.sql
// installs triggers that reject UPDATE, DELETE and TRUNCATE on it with
// SQLSTATE 23001 (restrict_violation) and a message starting with
// "audit_log is append-only", so a row, once written, cannot be rewritten by
// application code or an ad-hoc statement. The guarantee has limits: the
// table owner or a superuser can disable the triggers, and role separation,
// hash chaining and external shipping are out of scope. Any retention or
// purge must be an explicit owner-run procedure that disables the triggers,
// deletes and re-enables them inside one transaction; the recipe is in the
// header of 002_audit_log_append_only.sql. Integration tests that clean the
// table between cases must not TRUNCATE or DELETE it: use a fresh database
// per test or roll back a transaction instead.
//
// The schema was found byte-identical across go-bluprint, go-crucible, and
// go-licencias after six months of independent evolution — same columns,
// same defaults, same indexes — which is what makes it safe for this shared
// library to own rather than leaving each consumer to keep copy-pasting it.
//
// Consumers pass FS() to vogel/migrate.Up (or UpTo/UpByOne/Status) alongside
// their own application migrations' fs.FS. Because the library's version
// numbering starts at 001 independently of each application's own migration
// numbering, the two sets MUST run against separate goose version tables —
// see migrate.Options.TableName (FIX 4) and the "Running library migrations
// alongside application migrations" section of the top-level README.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS returns the embedded audit_log migration(s) as an fs.FS, ready to pass
// to vogel/migrate.Up.
func FS() fs.FS {
	return files
}

// DefaultTableName is the suggested goose version-table name for this
// library's migrations, kept separate from an application's own
// "goose_db_version" table so the two independently-numbered migration sets
// never collide. It is only a suggestion — migrate.Options.TableName is a
// caller-supplied option, not a hardcoded constant in vogel/migrate itself —
// but every consumer should use the same value so operators find one
// well-known table name across systems.
const DefaultTableName = "vogel_db_version"
