//go:build integration

package postgres_test

import (
	"context"
	"errors"
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
	"github.com/kafeiih/vogel/workflow/migrations"
	"github.com/kafeiih/vogel/workflow/postgres"
)

// startTestPostgres spins up a postgres:17-alpine testcontainer and returns
// its connection string. Mirrors worker/worker_integration_test.go and
// migrate/runner_integration_test.go so every package provisions its
// container the same way.
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

func silentOptions() migrate.Options {
	return migrate.Options{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		TableName: migrations.DefaultTableName,
	}
}

func startTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	connStr := startTestPostgres(t)

	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), silentOptions()), "apply workflow migrations")

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func newCase(domain, externalID string) *workflow.Case {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &workflow.Case{
		ID:         uuid.New(),
		Definition: "purchase",
		Version:    1,
		Domain:     domain,
		ExternalID: externalID,
		Unit:       "finance",
		State:      "draft",
		Status:     workflow.StatusOpen,
		OpenedAt:   now,
	}
}

func TestRepository_Create_GetByID_GetByExternalID(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	c := newCase("compras", "solicitud-1")
	require.NoError(t, repo.Create(ctx, pool, c))

	byID, err := repo.GetByID(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.ID, byID.ID)
	assert.Equal(t, c.Domain, byID.Domain)
	assert.Equal(t, c.ExternalID, byID.ExternalID)
	assert.Equal(t, workflow.StatusOpen, byID.Status)

	byExt, err := repo.GetByExternalID(ctx, pool, "compras", "solicitud-1")
	require.NoError(t, err)
	assert.Equal(t, c.ID, byExt.ID)
}

func TestRepository_GetByID_NotFound_ReturnsErrCaseNotFound(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	_, err := repo.GetByID(ctx, pool, uuid.New())
	require.Error(t, err)
	assert.True(t, errors.Is(err, workflow.ErrCaseNotFound))
}

func TestRepository_Create_DuplicateExternalReference_ReturnsErrCaseExists(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, pool, newCase("compras", "solicitud-dup")))

	err := repo.Create(ctx, pool, newCase("compras", "solicitud-dup"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, workflow.ErrCaseExists))
}

func TestRepository_Update_PersistsMutableFields(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	c := newCase("compras", "solicitud-2")
	require.NoError(t, repo.Create(ctx, pool, c))

	c.State = "review"
	c.AssignedTo = "u1"
	deadline := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)
	c.DeadlineAt = &deadline
	require.NoError(t, repo.Update(ctx, pool, c))

	got, err := repo.GetByID(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "review", got.State)
	assert.Equal(t, "u1", got.AssignedTo)
	require.NotNil(t, got.DeadlineAt)
	assert.WithinDuration(t, deadline, *got.DeadlineAt, time.Second)
}

func TestRepository_AppendEvent_And_ListEvents_Ordering(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	c := newCase("compras", "solicitud-3")
	require.NoError(t, repo.Create(ctx, pool, c))

	kinds := []workflow.EventKind{workflow.EventOpened, workflow.EventMoved, workflow.EventAssigned}
	for _, kind := range kinds {
		evt := &workflow.Event{
			ID:         uuid.New(),
			CaseID:     c.ID,
			Kind:       kind,
			OccurredAt: time.Now().UTC(),
		}
		require.NoError(t, repo.AppendEvent(ctx, pool, evt))
		assert.NotZero(t, evt.Seq, "AppendEvent must populate Seq from the persisted value")
	}

	events, err := repo.ListEvents(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, events, 3)
	for i, evt := range events {
		assert.Equal(t, int64(i+1), evt.Seq)
	}
	assert.Equal(t, workflow.EventOpened, events[0].Kind)
	assert.Equal(t, workflow.EventMoved, events[1].Kind)
	assert.Equal(t, workflow.EventAssigned, events[2].Kind)
}

func TestRepository_ListByEligibility_FiltersByUnassigned(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	claimed := newCase("compras", "solicitud-claimed")
	claimed.AssignedTo = "u1"
	require.NoError(t, repo.Create(ctx, pool, claimed))

	unclaimed := newCase("compras", "solicitud-unclaimed")
	require.NoError(t, repo.Create(ctx, pool, unclaimed))

	got, err := repo.ListByEligibility(ctx, pool, workflow.InboxFilter{Domain: "compras", Unassigned: true})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, unclaimed.ID, got[0].ID)
}

func TestRepository_ListByEligibility_FiltersByOverdue(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	overdue := newCase("compras", "solicitud-overdue")
	past := time.Now().Add(-1 * time.Hour).UTC().Truncate(time.Microsecond)
	overdue.DeadlineAt = &past
	require.NoError(t, repo.Create(ctx, pool, overdue))

	notOverdue := newCase("compras", "solicitud-not-overdue")
	future := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	notOverdue.DeadlineAt = &future
	require.NoError(t, repo.Create(ctx, pool, notOverdue))

	got, err := repo.ListByEligibility(ctx, pool, workflow.InboxFilter{Domain: "compras", Overdue: true})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, overdue.ID, got[0].ID)
}
