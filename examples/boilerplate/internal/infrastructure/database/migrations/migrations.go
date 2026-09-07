// Package migrations embeds this application's own SQL migration files
// (distinct from vogel/audit/migrations, which owns audit_log). This file
// exists solely to expose the embedded FS — it contains no business logic.
package migrations

import "embed"

// FS holds all of this application's SQL migration files embedded at build time.
//
//go:embed *.sql
var FS embed.FS
