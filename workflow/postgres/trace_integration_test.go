//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/workflow"
	"github.com/kafeiih/vogel/workflow/migrations"
	"github.com/kafeiih/vogel/workflow/postgres"
)

// insertLegacyEvent writes an event the way a binary from before migration
// 004 does: no schema_version, no detail.
func insertLegacyEvent(t *testing.T, pool *pgxpool.Pool, caseID uuid.UUID, seq int64, kind workflow.EventKind) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
        INSERT INTO workflow_event (id, case_id, seq, kind, from_state, to_state, action, actor_id, occurred_at, comment)
        VALUES ($1, $2, $3, $4, '', 'draft', '', 'u1', now(), '')`,
		uuid.New(), caseID, seq, string(kind))
	require.NoError(t, err)
}

func TestListEvents_LegacyRowReadsAsSchemaVersion1(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	c := newCase("compras", "legacy-1")
	require.NoError(t, repo.Create(ctx, pool, c))
	insertLegacyEvent(t, pool, c.ID, 1, workflow.EventOpened)

	events, err := repo.ListEvents(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, 1, events[0].SchemaVersion)
	assert.JSONEq(t, `{}`, string(events[0].Detail))
}

func TestRepository_AppendEvent_RoundTripsSchemaVersionAndDetail(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	c := newCase("compras", "detail-1")
	require.NoError(t, repo.Create(ctx, pool, c))

	withDetail := &workflow.Event{
		ID: uuid.New(), CaseID: c.ID, Seq: 1, Kind: workflow.EventAssigned,
		ActorID: "u1", OccurredAt: time.Now().UTC(),
		SchemaVersion: workflow.CurrentEventSchema,
		Detail:        []byte(`{"from":"","to":"u1"}`),
	}
	require.NoError(t, repo.AppendEvent(ctx, pool, withDetail))
	withoutDetail := &workflow.Event{
		ID: uuid.New(), CaseID: c.ID, Seq: 2, Kind: workflow.EventMoved,
		OccurredAt: time.Now().UTC(), SchemaVersion: workflow.CurrentEventSchema,
	}
	require.NoError(t, repo.AppendEvent(ctx, pool, withoutDetail))

	events, err := repo.ListEvents(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, workflow.CurrentEventSchema, events[0].SchemaVersion)
	assert.JSONEq(t, `{"from":"","to":"u1"}`, string(events[0].Detail))
	assert.JSONEq(t, `{}`, string(events[1].Detail), "an empty Detail must be stored as {}")
}

func TestRepository_AppendEvent_TakenSeqFails(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	c := newCase("compras", "seq-taken")
	require.NoError(t, repo.Create(ctx, pool, c))
	insertLegacyEvent(t, pool, c.ID, 1, workflow.EventOpened)

	err := repo.AppendEvent(ctx, pool, &workflow.Event{
		ID: uuid.New(), CaseID: c.ID, Seq: 1, Kind: workflow.EventMoved, OccurredAt: time.Now().UTC(),
	})
	require.Error(t, err, "a Seq already taken must fail, not be moved to the next free one")
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "got %v", err)
	assert.Equal(t, "23505", pgErr.Code)
}

func TestRepository_CreateAndUpdate_PersistLastSeq(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	c := newCase("compras", "last-seq")
	c.LastSeq = 1
	require.NoError(t, repo.Create(ctx, pool, c))
	got, err := repo.GetByID(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.LastSeq)

	got.LastSeq = 4
	require.NoError(t, repo.Update(ctx, pool, got))
	got, err = repo.GetByID(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(4), got.LastSeq)
}

func TestEngine_HistoryAcrossLegacyAndNewEvents(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	e := workflow.New(repo, workflow.Config{}, nil, workflow.WithDefinitions(workflow.Definition{
		Name: "purchase", Version: 1,
		Nodes: []workflow.Node{
			{ID: "draft", Start: true, Eligible: workflow.ByPosition("buyer")},
			{ID: "done", Terminal: true},
		},
		Transitions: []workflow.Transition{{From: "draft", To: "done", Action: "submit"}},
	}))

	opened, err := e.OpenDetailed(ctx, pool, workflow.OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "e2e-1", Unit: "finance", ActorID: "u1"})
	require.NoError(t, err)
	_, err = e.Claim(ctx, pool, opened.Case.ID, "u1")
	require.NoError(t, err)
	moved, err := e.MoveDetailed(ctx, pool, workflow.MoveInput{CaseID: opened.Case.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, int64(4), moved.Case.LastSeq)

	history, err := e.History(ctx, pool, opened.Case.ID)
	require.NoError(t, err)
	require.Len(t, history, 4)
	assert.Equal(t, moved.Events, history[2:])
	var d workflow.AssignmentDetail
	require.NoError(t, json.Unmarshal(history[1].Detail, &d))
	assert.Equal(t, workflow.AssignmentDetail{To: "u1"}, d)
}

func TestMigration004_BackfillsLastSeq(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	opts := silentOptions()
	require.NoError(t, migrate.UpTo(ctx, connStr, migrations.FS(), 3, opts))

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	withEvents, empty := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{withEvents, empty} {
		_, err := pool.Exec(ctx, `
            INSERT INTO workflow_case (id, definition, version, domain, external_id, unit, state, status, opened_at)
            VALUES ($1, 'purchase', 1, 'compras', $2, 'finance', 'draft', 'open', now())`, id, id.String())
		require.NoError(t, err)
	}
	for seq := int64(1); seq <= 3; seq++ {
		insertLegacyEvent(t, pool, withEvents, seq, workflow.EventMoved)
	}

	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), opts))

	repo := postgres.NewRepository()
	got, err := repo.GetByID(ctx, pool, withEvents)
	require.NoError(t, err)
	assert.Equal(t, int64(3), got.LastSeq)
	got, err = repo.GetByID(ctx, pool, empty)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got.LastSeq)

	events, err := repo.ListEvents(ctx, pool, withEvents)
	require.NoError(t, err)
	for _, ev := range events {
		assert.Equal(t, 1, ev.SchemaVersion)
	}
}
