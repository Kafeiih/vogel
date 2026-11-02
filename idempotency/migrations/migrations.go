// Package migrations embeds the SQL migration(s) that create the
// idempotency_key table vogel/idempotency/postgres uses: one row per
// (scope, key) an idempotent operation claimed, holding the request's
// fingerprint and, once the operation finished, the response it produced.
//
// Rows are not an audit trail: they expire (expires_at) and the consumer
// removes expired ones with idempotency/postgres.Store.DeleteExpired.
//
// Consumers pass FS() to vogel/migrate.Up (or UpTo/UpByOne/Status) alongside
// their own application migrations' fs.FS. This set is numbered from 001
// independently of every other set, so it MUST run against its own goose
// version table — see DefaultTableName, migrate.Options.TableName, and the
// "Running library migrations alongside application migrations" section of
// the top-level README.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS returns the embedded idempotency migration(s) as an fs.FS, ready to
// pass to vogel/migrate.Up.
func FS() fs.FS {
	return files
}

// DefaultTableName is the suggested goose version-table name for this
// library's idempotency migrations, kept separate from an application's own
// "goose_db_version" table and from the other vogel sets' tables. It is only
// a suggestion — migrate.Options.TableName is caller-supplied — but every
// consumer should use the same value so operators find one well-known table
// name across systems.
const DefaultTableName = "idempotency_db_version"
