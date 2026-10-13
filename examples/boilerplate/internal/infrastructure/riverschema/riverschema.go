// Package riverschema applies River's schema for this service. It is the one
// place both "api migrate up" and "worker start-from-migrate" go through, so
// the two paths cannot drift apart.
package riverschema

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kafeiih/vogel/worker"
)

// Migrate creates schema if it does not exist and applies River's own
// migrations inside it. Both steps are idempotent. The connecting role needs
// CREATE on the database (for the schema) and on the schema (for River's
// tables), so call this from the migrate phase with a migrator role, not from
// a serving worker.
func Migrate(ctx context.Context, pool *pgxpool.Pool, schema string, logger *slog.Logger) error {
	// River creates its tables INSIDE schema but does not create the schema
	// itself. On a freshly provisioned DB Migrate would fail with SQLSTATE
	// 3F000.
	if err := worker.EnsureSchema(ctx, pool, schema); err != nil {
		return fmt.Errorf("ensuring river schema: %w", err)
	}

	// An insert-only queue (nil workers) is enough to run the migrator; only
	// the schema matters here.
	queue, err := worker.NewRiverQueue(pool, nil, worker.Config{Schema: schema}, logger)
	if err != nil {
		return fmt.Errorf("creating river migrator: %w", err)
	}
	if err := queue.Migrate(ctx); err != nil {
		return fmt.Errorf("running river migrations: %w", err)
	}
	return nil
}
