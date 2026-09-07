// Package migrations embeds the SQL migration(s) that create the audit_log
// table this library owns.
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
