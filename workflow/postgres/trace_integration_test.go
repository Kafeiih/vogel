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
	assert.Equal(t, workflow.Actor{Kind: workflow.ActorUnspecified, ID: "u1"}, events[0].Actor)
	assert.Equal(t, "u1", events[0].ActorID)
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

func TestEngine_HistoryReadsLegacyAndNewEvents(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	// Postgres keeps microseconds in UTC-normalized form; a truncated UTC clock
	// lets the appended events compare equal to the ones read back.
	now := time.Now().UTC().Truncate(time.Microsecond)
	e := workflow.New(repo, workflow.Config{Now: func() time.Time { return now }}, nil, workflow.WithDefinitions(workflow.Definition{
		Name: "purchase", Version: 1,
		Nodes: []workflow.Node{
			{ID: "draft", Start: true, Eligible: workflow.ByPosition("buyer")},
			{ID: "done", Terminal: true},
		},
		Transitions: []workflow.Transition{{From: "draft", To: "done", Action: "submit"}},
	}))

	opened, err := e.OpenDetailed(ctx, pool, workflow.OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "e2e-1", Unit: "finance", ActorID: "u1"})
	require.NoError(t, err)
	caseID := opened.Case.ID

	// A pre-004 binary appended seq 2; the operator's backfill caught last_seq up.
	insertLegacyEvent(t, pool, caseID, 2, workflow.EventAssigned)
	_, err = pool.Exec(ctx, `UPDATE workflow_case SET last_seq = 2 WHERE id = $1`, caseID)
	require.NoError(t, err)

	_, err = e.Claim(ctx, pool, caseID, "u1")
	require.NoError(t, err)
	moved, err := e.MoveDetailed(ctx, pool, workflow.MoveInput{CaseID: caseID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, int64(5), moved.Case.LastSeq)

	history, err := e.History(ctx, pool, caseID)
	require.NoError(t, err)
	require.Len(t, history, 5)
	for i, ev := range history {
		assert.Equal(t, int64(i+1), ev.Seq)
		want := workflow.CurrentEventSchema
		if i == 1 {
			want = 1
		}
		assert.Equal(t, want, ev.SchemaVersion, "seq %d", ev.Seq)
	}
	assert.JSONEq(t, `{}`, string(history[1].Detail), "legacy row reads with an empty detail")

	var d workflow.AssignmentDetail
	require.NoError(t, json.Unmarshal(history[2].Detail, &d))
	assert.Equal(t, workflow.AssignmentDetail{To: "u1"}, d)

	require.Len(t, moved.Events, 2)
	for i, ev := range moved.Events {
		got := history[3+i]
		assert.Equal(t, ev.ID, got.ID)
		assert.Equal(t, ev.Seq, got.Seq)
		assert.Equal(t, ev.Kind, got.Kind)
		assert.True(t, ev.OccurredAt.Equal(got.OccurredAt), "seq %d: %v vs %v", ev.Seq, ev.OccurredAt, got.OccurredAt)
	}
}

func TestRepository_AppendEvent_ZeroSchemaVersionStoredAs1(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	c := newCase("compras", "schema-zero")
	require.NoError(t, repo.Create(ctx, pool, c))
	require.NoError(t, repo.AppendEvent(ctx, pool, &workflow.Event{
		ID: uuid.New(), CaseID: c.ID, Seq: 1, Kind: workflow.EventOpened, OccurredAt: time.Now().UTC(),
	}))

	var stored int
	require.NoError(t, pool.QueryRow(ctx, `SELECT schema_version FROM workflow_event WHERE case_id = $1`, c.ID).Scan(&stored))
	assert.Equal(t, 1, stored)
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

// tenantTestEngine is an engine over the postgres repository with a one-step
// definition, for the tenant and actor tests.
func tenantTestEngine(t *testing.T, repo *postgres.Repository) *workflow.Engine {
	t.Helper()
	return workflow.New(repo, workflow.Config{}, nil, workflow.WithDefinitions(workflow.Definition{
		Name: "purchase", Version: 1,
		Nodes: []workflow.Node{
			{ID: "draft", Start: true, Eligible: workflow.ByPosition("buyer")},
			{ID: "done", Terminal: true},
		},
		Transitions: []workflow.Transition{{From: "draft", To: "done", Action: "submit"}},
	}))
}

func TestEvent_PersistsActingAsAndOnBehalfOf(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()
	e := tenantTestEngine(t, repo)

	opener := workflow.Actor{Kind: workflow.ActorOperator, ID: "op-1", ActingAs: "jefe@finanzas:subrogante", OnBehalfOf: "u9", Via: "portal"}
	opened, err := e.Open(ctx, pool, workflow.OpenInput{
		Definition: "purchase", Tenant: "tenant-a", Domain: "compras", ExternalID: "actor-1", Unit: "finance", Actor: opener,
	})
	require.NoError(t, err)
	_, err = e.ClaimWith(ctx, pool, workflow.ClaimInput{CaseID: opened.ID, Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u2"}})
	require.NoError(t, err)
	_, err = e.ReleaseWith(ctx, pool, workflow.ReleaseInput{
		CaseID: opened.ID, Actor: workflow.Actor{Kind: workflow.ActorSystem, ID: "deadline-sweeper"}, Reason: "expired",
	})
	require.NoError(t, err)

	history, err := e.History(ctx, pool, opened.ID)
	require.NoError(t, err)
	require.Len(t, history, 3)
	assert.Equal(t, opener, history[0].Actor)
	assert.Equal(t, "op-1", history[0].ActorID)
	assert.Equal(t, workflow.Actor{Kind: workflow.ActorHuman, ID: "u2"}, history[1].Actor)
	assert.Equal(t, workflow.Actor{Kind: workflow.ActorSystem, ID: "deadline-sweeper"}, history[2].Actor)
	for _, ev := range history {
		assert.Equal(t, "tenant-a", ev.Tenant, "seq %d", ev.Seq)
	}

	var d workflow.AssignmentDetail
	require.NoError(t, json.Unmarshal(history[2].Detail, &d))
	assert.Equal(t, workflow.AssignmentDetail{From: "u2", Reason: "expired"}, d)
}

func TestOpen_SameExternalIDInTwoTenantsIsAllowed(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()
	e := tenantTestEngine(t, repo)

	open := func(tenant string) (*workflow.Case, error) {
		return e.Open(ctx, pool, workflow.OpenInput{
			Definition: "purchase", Tenant: tenant, Domain: "compras", ExternalID: "solicitud-1", Unit: "finance", ActorID: "u1",
		})
	}
	a, err := open("tenant-a")
	require.NoError(t, err)
	b, err := open("tenant-b")
	require.NoError(t, err)
	_, err = open("tenant-a")
	assert.True(t, errors.Is(err, workflow.ErrCaseExists), "same tenant must still be unique, got %v", err)

	got, err := repo.GetByExternalID(ctx, pool, "tenant-b", "compras", "solicitud-1")
	require.NoError(t, err)
	assert.Equal(t, b.ID, got.ID)
	assert.Equal(t, "tenant-b", got.Tenant)
	got, err = repo.GetByExternalID(ctx, pool, "tenant-a", "compras", "solicitud-1")
	require.NoError(t, err)
	assert.Equal(t, a.ID, got.ID)
	_, err = repo.GetByExternalID(ctx, pool, "", "compras", "solicitud-1")
	assert.True(t, errors.Is(err, workflow.ErrCaseNotFound), "got %v", err)
}

func TestRepository_ListByEligibility_FiltersByTenant(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	a := newCase("compras", "tenant-list-1")
	a.Tenant = "tenant-a"
	b := newCase("compras", "tenant-list-1")
	b.Tenant = "tenant-b"
	require.NoError(t, repo.Create(ctx, pool, a))
	require.NoError(t, repo.Create(ctx, pool, b))

	got, err := repo.ListByEligibility(ctx, pool, workflow.InboxFilter{Tenant: "tenant-a"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, a.ID, got[0].ID)

	got, err = repo.ListByEligibility(ctx, pool, workflow.InboxFilter{Domain: "compras"})
	require.NoError(t, err)
	assert.Len(t, got, 2, "an empty tenant matches every tenant")
}
