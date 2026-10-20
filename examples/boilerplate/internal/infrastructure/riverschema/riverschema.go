package riverschema

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kafeiih/vogel/worker"
)

func Migrate(ctx context.Context, pool *pgxpool.Pool, schema string, logger *slog.Logger) error {
	if err := worker.EnsureSchema(ctx, pool, schema); err != nil {
		return fmt.Errorf("ensuring river schema: %w", err)
	}

	queue, err := worker.NewRiverQueue(pool, nil, worker.Config{Schema: schema}, logger)
	if err != nil {
		return fmt.Errorf("creating river migrator: %w", err)
	}
	if err := queue.Migrate(ctx); err != nil {
		return fmt.Errorf("running river migrations: %w", err)
	}
	return nil
}
