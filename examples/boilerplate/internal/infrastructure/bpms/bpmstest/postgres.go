//go:build integration

package bpmstest

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/workflow"
	wfmigrations "github.com/kafeiih/vogel/workflow/migrations"
	wfpostgres "github.com/kafeiih/vogel/workflow/postgres"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/bpms/migrations"
)

func Logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("test_db"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	if err := migrate.Up(ctx, dsn, wfmigrations.FS(), migrate.Options{Logger: Logger(), TableName: wfmigrations.DefaultTableName}); err != nil {
		t.Fatalf("workflow migrations: %v", err)
	}
	if err := migrate.Up(ctx, dsn, migrations.FS, migrate.Options{Logger: Logger(), TableName: migrations.TableName}); err != nil {
		t.Fatalf("bpms migrations: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func NewEngine() *workflow.Engine {
	return workflow.New(wfpostgres.NewRepository(), workflow.Config{}, Logger())
}
