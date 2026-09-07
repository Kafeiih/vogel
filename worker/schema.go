package worker

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaNamePattern enforces a strict subset of valid PostgreSQL identifier
// characters before EnsureSchema interpolates the name into DDL. pgx.Identifier
// only quotes; it does not validate, so a value from configuration must be
// pattern-checked first to prevent injection through control characters or
// chained statements.
var schemaNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// isValidSchemaName reports whether s is a safe PostgreSQL schema identifier
// for use with EnsureSchema. Empty strings, names starting with a digit,
// names with whitespace or punctuation, and names longer than the Postgres
// 63-byte identifier limit are rejected.
func isValidSchemaName(s string) bool {
	return s != "" && len(s) <= 63 && schemaNamePattern.MatchString(s)
}

// EnsureSchema creates the PostgreSQL schema if it does not exist. Idempotent
// and safe to call on every worker startup. River creates its tables INSIDE
// the configured schema but does not create the schema itself, so this must
// run before RiverQueue.Migrate on a freshly provisioned database.
func EnsureSchema(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	if !isValidSchemaName(schema) {
		return fmt.Errorf("worker: invalid schema name %q (must match %s)",
			schema, schemaNamePattern.String())
	}
	if pool == nil {
		return fmt.Errorf("worker: pool is nil")
	}
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+quoted); err != nil {
		return fmt.Errorf("worker: creating schema %q: %w", schema, err)
	}
	return nil
}
