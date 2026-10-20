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

	"github.com/kafeiih/vogel/document"
	"github.com/kafeiih/vogel/document/migrations"
	"github.com/kafeiih/vogel/document/postgres"
	"github.com/kafeiih/vogel/migrate"
)

// startTestPostgres spins up a postgres:17-alpine testcontainer and returns
// its connection string. Mirrors workflow/postgres's integration test so
// every package provisions its container the same way.
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

	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), silentOptions()), "apply document migrations")

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func newDocument(caseID *uuid.UUID, createdAt time.Time) *document.Document {
	id := uuid.New()
	return &document.Document{
		ID:              id,
		Domain:          "compras",
		ExternalID:      "solicitud-1",
		CaseID:          caseID,
		TemplateName:    "acta",
		TemplateVersion: 2,
		SHA256:          "ab12",
		StorageKey:      "documents/compras/" + id.String() + ".pdf",
		VerificationKey: "vk-" + id.String(),
		Size:            1234,
		CreatedAt:       createdAt.UTC().Truncate(time.Microsecond),
	}
}

func TestRepository_Insert_GetByID_RoundTrips(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	caseID := uuid.New()
	d := newDocument(&caseID, time.Now())
	require.NoError(t, repo.Insert(ctx, pool, d))

	got, err := repo.GetByID(ctx, pool, d.ID)
	require.NoError(t, err)
	assert.Equal(t, d.CreatedAt, got.CreatedAt.UTC())
	got.CreatedAt = d.CreatedAt
	assert.Equal(t, d, got)
}

func TestRepository_Insert_NullCaseID_RoundTrips(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	d := newDocument(nil, time.Now())
	require.NoError(t, repo.Insert(ctx, pool, d))

	got, err := repo.GetByID(ctx, pool, d.ID)
	require.NoError(t, err)
	assert.Nil(t, got.CaseID)
}

func TestRepository_GetByID_NotFound_ReturnsErrNotFound(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()

	_, err := repo.GetByID(context.Background(), pool, uuid.New())
	require.ErrorIs(t, err, document.ErrNotFound)
}

func TestRepository_Insert_DuplicateVerificationKey_IsError(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	a := newDocument(nil, time.Now())
	require.NoError(t, repo.Insert(ctx, pool, a))
	b := newDocument(nil, time.Now())
	b.VerificationKey = a.VerificationKey
	require.Error(t, repo.Insert(ctx, pool, b))
}

func TestRepository_ListByCase_OrdersOldestFirstAndFiltersByCase(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	caseID, other := uuid.New(), uuid.New()
	base := time.Now()
	newer := newDocument(&caseID, base.Add(time.Minute))
	older := newDocument(&caseID, base)
	require.NoError(t, repo.Insert(ctx, pool, newer))
	require.NoError(t, repo.Insert(ctx, pool, older))
	require.NoError(t, repo.Insert(ctx, pool, newDocument(&other, base)))

	list, err := repo.ListByCase(ctx, pool, caseID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, older.ID, list[0].ID)
	assert.Equal(t, newer.ID, list[1].ID)

	empty, err := repo.ListByCase(ctx, pool, uuid.New())
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestRepository_GetByVerificationKey(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	d := newDocument(nil, time.Now())
	require.NoError(t, repo.Insert(ctx, pool, d))

	got, err := repo.GetByVerificationKey(ctx, pool, d.VerificationKey)
	require.NoError(t, err)
	assert.Equal(t, d.ID, got.ID)

	_, err = repo.GetByVerificationKey(ctx, pool, "missing")
	require.ErrorIs(t, err, document.ErrNotFound)
}

func TestMigrations_DownRemovesTable(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), silentOptions()))
	require.NoError(t, migrate.Down(ctx, connStr, migrations.FS(), silentOptions()))

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	defer pool.Close()

	var exists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.document_record') IS NOT NULL`).Scan(&exists))
	assert.False(t, exists)
}

func TestMigrations_CreatePrefixedTable(t *testing.T) {
	pool := startTestPool(t)
	var prefixed, bare bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT to_regclass('public.document_record') IS NOT NULL, to_regclass('public.document') IS NOT NULL`).Scan(&prefixed, &bare))
	assert.True(t, prefixed, "metadata lives in document_record")
	assert.False(t, bare, "no bare document table that could collide with a consumer's own")
}

func TestRepository_GetBySourceKey_RoundTripsAndEmptyKeysDoNotCollide(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	caseID := uuid.New()
	d := newDocument(&caseID, time.Now())
	d.SourceKey = caseID.String() + "/3/acta"
	require.NoError(t, repo.Insert(ctx, pool, d))

	got, err := repo.GetBySourceKey(ctx, pool, d.SourceKey)
	require.NoError(t, err)
	assert.Equal(t, d.ID, got.ID)
	assert.Equal(t, d.SourceKey, got.SourceKey)

	dup := newDocument(&caseID, time.Now())
	dup.SourceKey = d.SourceKey
	require.Error(t, repo.Insert(ctx, pool, dup), "a source key is unique")

	require.NoError(t, repo.Insert(ctx, pool, newDocument(&caseID, time.Now())))
	require.NoError(t, repo.Insert(ctx, pool, newDocument(&caseID, time.Now())), "empty source keys never collide")

	_, err = repo.GetBySourceKey(ctx, pool, "missing")
	require.ErrorIs(t, err, document.ErrNotFound)
	_, err = repo.GetBySourceKey(ctx, pool, "")
	require.ErrorIs(t, err, document.ErrNotFound, "an empty key never matches the documents stored without one")
}

func TestRepository_ExistsForCase_MatchesCaseAndTemplate(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	caseID := uuid.New()
	require.NoError(t, repo.Insert(ctx, pool, newDocument(&caseID, time.Now())))

	ok, err := repo.ExistsForCase(ctx, pool, caseID, "acta")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = repo.ExistsForCase(ctx, pool, caseID, "orden")
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = repo.ExistsForCase(ctx, pool, uuid.New(), "acta")
	require.NoError(t, err)
	assert.False(t, ok)
}
