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
	for i, kind := range kinds {
		evt := &workflow.Event{
			ID:         uuid.New(),
			CaseID:     c.ID,
			Seq:        int64(i + 1),
			Kind:       kind,
			OccurredAt: time.Now().UTC(),
		}
		require.NoError(t, repo.AppendEvent(ctx, pool, evt))
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

func TestRepository_AppendEvent_And_ListEvents_PersistsComment(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	c := newCase("compras", "solicitud-comment")
	require.NoError(t, repo.Create(ctx, pool, c))

	withComment := &workflow.Event{
		ID:         uuid.New(),
		CaseID:     c.ID,
		Seq:        1,
		Kind:       workflow.EventMoved,
		Action:     "approve",
		Comment:    "budget looks fine",
		OccurredAt: time.Now().UTC(),
	}
	require.NoError(t, repo.AppendEvent(ctx, pool, withComment))

	withoutComment := &workflow.Event{
		ID:         uuid.New(),
		CaseID:     c.ID,
		Seq:        2,
		Kind:       workflow.EventClosed,
		Action:     "approve",
		OccurredAt: time.Now().UTC(),
	}
	require.NoError(t, repo.AppendEvent(ctx, pool, withoutComment))

	events, err := repo.ListEvents(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, "budget looks fine", events[0].Comment, "comment must round-trip through AppendEvent/ListEvents")
	assert.Equal(t, "", events[1].Comment, "an event appended with no comment must read back empty, not NULL")
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

func TestRepository_Create_StartsAtRevisionZero(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	c := newCase("compras", "solicitud-rev0")
	c.Revision = 42 // Create ignores any caller-supplied revision.
	require.NoError(t, repo.Create(ctx, pool, c))
	assert.Equal(t, int64(0), c.Revision)

	got, err := repo.GetByID(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got.Revision)
}

func TestRepository_Update_StaleRevision_ReturnsErrConflict(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	c := newCase("compras", "solicitud-race")
	require.NoError(t, repo.Create(ctx, pool, c))

	// Two independent loads of the same case, as two concurrent callers
	// would hold them.
	first, err := repo.GetByID(ctx, pool, c.ID)
	require.NoError(t, err)
	second, err := repo.GetByID(ctx, pool, c.ID)
	require.NoError(t, err)

	first.AssignedTo = "u1"
	require.NoError(t, repo.Update(ctx, pool, first))
	assert.Equal(t, int64(1), first.Revision, "a successful update advances the in-memory revision")

	second.AssignedTo = "u2"
	second.State = "review"
	err = repo.Update(ctx, pool, second)
	require.Error(t, err)
	assert.True(t, errors.Is(err, workflow.ErrConflict), "want ErrConflict, got %v", err)
	assert.False(t, errors.Is(err, workflow.ErrCaseNotFound))
	assert.Equal(t, int64(0), second.Revision, "a conflicting update leaves the caller's revision untouched")

	got, err := repo.GetByID(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "u1", got.AssignedTo, "the first writer's update survives")
	assert.Equal(t, "draft", got.State, "the stale write is not applied")
	assert.Equal(t, int64(1), got.Revision)

	// The first caller, holding the advanced revision, can keep writing.
	first.State = "review"
	require.NoError(t, repo.Update(ctx, pool, first))
	assert.Equal(t, int64(2), first.Revision)
}

func TestRepository_Update_MissingCase_ReturnsErrCaseNotFound(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	err := repo.Update(ctx, pool, newCase("compras", "solicitud-missing"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, workflow.ErrCaseNotFound), "want ErrCaseNotFound, got %v", err)
	assert.False(t, errors.Is(err, workflow.ErrConflict))
}

func TestMigrations_003_AddsRevisionColumn(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()

	var dataType, nullable, def string
	err := pool.QueryRow(ctx, `
        SELECT data_type, is_nullable, column_default
        FROM information_schema.columns
        WHERE table_name = 'workflow_case' AND column_name = 'revision'`).Scan(&dataType, &nullable, &def)
	require.NoError(t, err, "migration 003 must add workflow_case.revision")
	assert.Equal(t, "bigint", dataType)
	assert.Equal(t, "NO", nullable)
	assert.Equal(t, "0", def)
}
