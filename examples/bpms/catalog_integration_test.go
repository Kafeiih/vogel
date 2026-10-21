//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/workflow"
	wfmigrations "github.com/kafeiih/vogel/workflow/migrations"
	wfpg "github.com/kafeiih/vogel/workflow/postgres"
)

// startTestPostgres spins up a postgres:17-alpine testcontainer and returns
// its connection string, the same way examples/api provisions its own.
func startTestPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("test_db"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err, "start postgres container")
	t.Cleanup(func() { _ = pgContainer.Terminate(context.Background()) })

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return connStr
}

// newTestPool returns a pool on a fresh database with every migration set
// main.go applies at boot.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	connStr := startTestPostgres(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	appFS, err := appMigrationsFS()
	require.NoError(t, err)
	require.NoError(t, migrate.Up(ctx, connStr, appFS, migrate.Options{Logger: logger}))
	require.NoError(t, migrate.Up(ctx, connStr, wfmigrations.FS(), migrate.Options{
		Logger:    logger,
		TableName: wfmigrations.DefaultTableName,
	}))

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func newTestEngine() *workflow.Engine {
	return workflow.New(wfpg.NewRepository(), workflow.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestCatalogPublishAssignsIncreasingVersions(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	catalog := NewCatalog(testRegistry(t), newTestEngine())

	first, err := catalog.Publish(ctx, pool, loadConsultaSpec(t))
	require.NoError(t, err)
	second, err := catalog.Publish(ctx, pool, loadConsultaSpec(t))
	require.NoError(t, err)

	assert.Equal(t, 1, first.Definition.Version)
	assert.Equal(t, 2, second.Definition.Version)
	latest, ok := catalog.Latest("consulta")
	require.True(t, ok)
	assert.Equal(t, 2, latest.Definition.Version)
}

func TestCatalogPublishStoresNothingForAnInvalidSpec(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	catalog := NewCatalog(testRegistry(t), newTestEngine())

	spec := loadConsultaSpec(t)
	spec.Nodes[0].Form[0].Type = "rut"
	_, err := catalog.Publish(ctx, pool, spec)
	require.Error(t, err)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM process_version`).Scan(&n))
	assert.Zero(t, n)
}

func TestCatalogLoadRegistersEveryPublishedVersion(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	_, err := NewCatalog(testRegistry(t), newTestEngine()).Publish(ctx, pool, loadConsultaSpec(t))
	require.NoError(t, err)
	_, err = NewCatalog(testRegistry(t), newTestEngine()).Publish(ctx, pool, loadConsultaSpec(t))
	require.NoError(t, err)

	engine := newTestEngine()
	rebooted := NewCatalog(testRegistry(t), engine)
	require.NoError(t, rebooted.Load(ctx, pool))

	_, ok := rebooted.Get("consulta", 1)
	assert.True(t, ok)
	_, ok = rebooted.Get("consulta", 2)
	assert.True(t, ok)
	assert.Len(t, engine.Definitions(), 2)
}

func TestCaseValuesKeepHistory(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	caseID := uuid.New()

	require.NoError(t, SaveCaseValues(ctx, pool, caseID, "ingreso", "ana", map[string]any{"tema": "general", "nombre": "Ana"}))
	require.NoError(t, SaveCaseValues(ctx, pool, caseID, "respuesta", "luis", map[string]any{"tema": "reclamo"}))

	current, err := CurrentCaseValues(ctx, pool, caseID)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"tema": "reclamo", "nombre": "Ana"}, current)

	history, err := CaseValueHistory(ctx, pool, caseID)
	require.NoError(t, err)
	require.Len(t, history, 3)
	last := history[len(history)-1]
	assert.Equal(t, "tema", last.Field)
	assert.Equal(t, "reclamo", last.Value)
	assert.Equal(t, "respuesta", last.NodeID)
	assert.Equal(t, "luis", last.ActorID)
	assert.False(t, last.RecordedAt.IsZero())
}
