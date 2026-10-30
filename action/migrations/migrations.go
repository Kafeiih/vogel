// Package migrations embeds the SQL migration(s) that create the
// action_attempt table this library owns: one row per attempt of an effect
// job, written by action.Worker when it is built with action.WithAttemptLog.
//
// 001_create_action_attempt.sql creates the table append-only: triggers
// reject UPDATE, DELETE and TRUNCATE on it with SQLSTATE 23001
// (restrict_violation) and a message starting with "action_attempt is
// append-only", the same pattern as vogel/audit/migrations. The table owner
// or a superuser can still disable the triggers; any retention or purge must
// be an explicit owner-run procedure that disables the triggers, deletes and
// re-enables them inside one transaction (recipe in the migration's header).
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

// FS returns the embedded action migration(s) as an fs.FS, ready to pass to
// vogel/migrate.Up.
func FS() fs.FS {
	return files
}

// DefaultTableName is the suggested goose version-table name for this
// library's action migrations, kept separate from an application's own
// "goose_db_version" table and from the other vogel sets' tables. It is only
// a suggestion — migrate.Options.TableName is caller-supplied — but every
// consumer should use the same value so operators find one well-known table
// name across systems.
const DefaultTableName = "action_db_version"
