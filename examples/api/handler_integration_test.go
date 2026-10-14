//go:build integration

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/kafeiih/vogel/access"
	"github.com/kafeiih/vogel/audit"
	auditmigrations "github.com/kafeiih/vogel/audit/migrations"
	auditpg "github.com/kafeiih/vogel/audit/postgres"
	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/worker"
	"github.com/kafeiih/vogel/workflow"
	wfmigrations "github.com/kafeiih/vogel/workflow/migrations"
	wfpg "github.com/kafeiih/vogel/workflow/postgres"
)

// startTestPostgres spins up a postgres:17-alpine testcontainer and returns
// its connection string. Mirrors workflow/postgres and migrate's integration
// tests so every package provisions its container the same way.
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

// newIntegrationHandler builds a DocumentHandler against a fresh database,
// applying every migration set main.go applies at boot, each against its own
// goose version table, plus River's own schema and migrations.
//
// The queue is insert-only (workers == nil): Create only ever calls
// EnqueueTx, and not starting a worker keeps the job from running against the
// fake notifier while the test inspects the database.
func newIntegrationHandler(t *testing.T) (*DocumentHandler, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	connStr := startTestPostgres(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	appFS, err := appMigrationsFS()
	require.NoError(t, err)
	require.NoError(t, migrate.Up(ctx, connStr, appFS, migrate.Options{Logger: logger}), "apply app migrations")
	require.NoError(t, migrate.Up(ctx, connStr, auditmigrations.FS(), migrate.Options{
		Logger:    logger,
		TableName: auditmigrations.DefaultTableName,
	}), "apply audit migrations")
	require.NoError(t, migrate.Up(ctx, connStr, wfmigrations.FS(), migrate.Options{
		Logger:    logger,
		TableName: wfmigrations.DefaultTableName,
	}), "apply workflow migrations")

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	const riverSchema = "river"
	require.NoError(t, worker.EnsureSchema(ctx, pool, riverSchema), "ensure river schema")
	queue, err := worker.NewRiverQueue(pool, nil, worker.Config{Schema: riverSchema}, logger)
	require.NoError(t, err)
	require.NoError(t, queue.Migrate(ctx), "apply river migrations")

	store := NewDocumentStore()
	wfRepo := wfpg.NewRepository()
	engine := workflow.New(wfRepo, workflow.Config{}, logger, workflow.WithDefinitions(ApprovalDefinition()))
	require.NoError(t, engine.RegisterGuard("assigned", AssignedGuard))

	recorder := audit.NewRecorder(auditpg.NewRepository(pool))
	guard := access.New(fakeChecker{})

	h := NewDocumentHandler(store, engine, wfRepo, recorder, queue, newFakeStorage(),
		pgxtx.NewPgxTxManager(pool), pool, guard, logger)
	return h, pool
}

// TestDocumentHandlerCreate_PersistsThePrincipalAsOwner proves the
// security-relevant half of Create that handler_test.go cannot reach without
// a database: the owner_id stored on the documents row is the authenticated
// principal's UserID. Two distinct principals guard against a hardcoded owner.
func TestDocumentHandlerCreate_PersistsThePrincipalAsOwner(t *testing.T) {
	h, pool := newIntegrationHandler(t)

	tests := []struct {
		name   string
		userID string
	}{
		{name: "first principal owns its document", userID: "user-alice"},
		{name: "a different principal owns its own document", userID: "user-bob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"title":"Contrato","kind":"contract","issued_at":"2026-01-02"}`
			req := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(auth.WithPrincipal(req.Context(), &auth.Principal{UserID: tt.userID}))
			rec := httptest.NewRecorder()

			h.Create(rec, req)

			require.Equal(t, http.StatusCreated, rec.Code, "body %s", rec.Body.String())

			var resp struct {
				Data Document `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.NotEqual(t, uuid.Nil, resp.Data.ID, "response carries the created document ID")

			var ownerID string
			err := pool.QueryRow(context.Background(),
				`SELECT owner_id FROM documents WHERE id = $1`, resp.Data.ID).Scan(&ownerID)
			require.NoError(t, err, "read back the created document")
			assert.Equal(t, tt.userID, ownerID, "persisted owner_id is the principal's UserID")
		})
	}
}
