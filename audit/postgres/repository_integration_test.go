//go:build integration

package postgres_test

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

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/audit/migrations"
	auditpg "github.com/kafeiih/vogel/audit/postgres"
	"github.com/kafeiih/vogel/migrate"
)

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

// newTestRepository migrates the audit_log schema (via the same
// vogel/audit/migrations + vogel/migrate this library ships) against a fresh
// container and returns a ready Repository plus its pool.
func newTestRepository(t *testing.T) (*auditpg.Repository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	connStr := startTestPostgres(t)

	silent := migrate.Options{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		TableName: migrations.DefaultTableName,
	}
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), silent), "apply audit_log migration")

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return auditpg.NewRepository(pool), pool
}

func TestRepository_CreateAndGetByID_RoundTripsRequestID(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	requestID := "req-xyz"
	entry := &audit.Entry{
		ID:                uuid.New(),
		ResourceType:      "widget",
		ResourceID:        "w-1",
		OperationCategory: string(audit.ActionCreate),
		OperationName:     "create_widget",
		Status:            string(audit.StatusSuccess),
		ActorID:           "u-1",
		Username:          "alice",
		Source:            string(audit.SourceHTTP),
		RequestID:         &requestID,
		IP:                "10.0.0.1",
		UserAgent:         "curl/8",
		ObjectRepr:        "Widget #1",
		AfterSnapshot:     map[string]any{"name": "gizmo"},
		CreatedAt:         time.Now().UTC().Truncate(time.Microsecond),
	}

	require.NoError(t, repo.Create(ctx, pool, entry))

	got, err := repo.GetByID(ctx, entry.ID)
	require.NoError(t, err)

	require.NotNil(t, got.RequestID, "request_id must round-trip, not be NULL")
	assert.Equal(t, requestID, *got.RequestID)
	assert.Equal(t, entry.ActorID, got.ActorID)
	assert.Equal(t, entry.Source, got.Source)
	assert.Equal(t, "gizmo", got.AfterSnapshot["name"])
}

func TestRepository_Create_WorkerSource_NilRequestID(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	entry := &audit.Entry{
		ID:                uuid.New(),
		ResourceType:      "job",
		ResourceID:        "j-1",
		OperationCategory: string(audit.ActionExecute),
		OperationName:     "run_job",
		Status:            string(audit.StatusSuccess),
		Source:            string(audit.SourceWorker),
		CreatedAt:         time.Now().UTC().Truncate(time.Microsecond),
	}

	require.NoError(t, repo.Create(ctx, pool, entry))

	got, err := repo.GetByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Nil(t, got.RequestID, "a worker-originated entry may legitimately have no request ID")
	assert.Equal(t, string(audit.SourceWorker), got.Source)
}

func TestRepository_List_FiltersByTypedResourceID_AndReturnsTotalViaWindowFunction(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	targetID := uuid.New()
	targetResourceID := targetID.String()

	// Two entries for the target resource, one for a different resource.
	for i := 0; i < 2; i++ {
		require.NoError(t, repo.Create(ctx, pool, &audit.Entry{
			ID:                uuid.New(),
			ResourceType:      "widget",
			ResourceID:        targetResourceID,
			OperationCategory: string(audit.ActionUpdate),
			OperationName:     "update_widget",
			Status:            string(audit.StatusSuccess),
			Source:            string(audit.SourceHTTP),
			CreatedAt:         time.Now().UTC(),
		}))
	}
	require.NoError(t, repo.Create(ctx, pool, &audit.Entry{
		ID:                uuid.New(),
		ResourceType:      "widget",
		ResourceID:        uuid.New().String(),
		OperationCategory: string(audit.ActionUpdate),
		OperationName:     "update_widget",
		Status:            string(audit.StatusSuccess),
		Source:            string(audit.SourceHTTP),
		CreatedAt:         time.Now().UTC(),
	}))

	entries, total, err := repo.List(ctx, audit.Filters{ResourceID: &targetID, Limit: 20})
	require.NoError(t, err)

	assert.Equal(t, 2, total, "count(*) OVER() must report the total matching rows, not the page size")
	assert.Len(t, entries, 2)
	for _, e := range entries {
		assert.Equal(t, targetResourceID, e.ResourceID)
	}
}

func TestRepository_List_Pagination_TotalIndependentOfLimit(t *testing.T) {
	repo, pool := newTestRepository(t)
	ctx := context.Background()

	resourceType := "paged-widget-" + uuid.NewString()
	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Create(ctx, pool, &audit.Entry{
			ID:                uuid.New(),
			ResourceType:      resourceType,
			ResourceID:        uuid.NewString(),
			OperationCategory: string(audit.ActionCreate),
			OperationName:     "create_widget",
			Status:            string(audit.StatusSuccess),
			Source:            string(audit.SourceHTTP),
			CreatedAt:         time.Now().UTC(),
		}))
	}

	entries, total, err := repo.List(ctx, audit.Filters{ResourceType: resourceType, Limit: 2, Offset: 0})
	require.NoError(t, err)
	assert.Len(t, entries, 2, "page size must respect Limit")
	assert.Equal(t, 5, total, "total must reflect all matching rows, not just the returned page")
}
