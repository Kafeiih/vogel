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
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/reqctx"
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
	assert.Equal(t, "u1", events[0].ActorID) //nolint:staticcheck // the deprecated mirror must still read back
}

func TestRepository_AppendEvent_RoundTripsSchemaVersionAndDetail(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := context.Background()

	c := newCase("compras", "detail-1")
	require.NoError(t, repo.Create(ctx, pool, c))

	withDetail := &workflow.Event{
		ID: uuid.New(), CaseID: c.ID, Seq: 1, Kind: workflow.EventAssigned,
		Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}, OccurredAt: time.Now().UTC(),
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
	assert.ErrorIs(t, err, workflow.ErrConflict, "a taken seq is a concurrent append")
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

	opened, err := e.OpenDetailed(ctx, pool, workflow.OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "e2e-1", Unit: "finance", Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}})
	require.NoError(t, err)
	caseID := opened.Case.ID

	// A pre-004 binary appended seq 2; the operator's backfill caught last_seq up.
	insertLegacyEvent(t, pool, caseID, 2, workflow.EventAssigned)
	_, err = pool.Exec(ctx, `UPDATE workflow_case SET last_seq = 2 WHERE id = $1`, caseID)
	require.NoError(t, err)

	_, err = e.Claim(ctx, pool, caseID, "u1")
	require.NoError(t, err)
	moved, err := e.MoveDetailed(ctx, pool, workflow.MoveInput{CaseID: caseID, Action: "submit", Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}})
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
	assert.Equal(t, "op-1", history[0].ActorID) //nolint:staticcheck // the deprecated mirror must still read back
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
			Definition: "purchase", Tenant: tenant, Domain: "compras", ExternalID: "solicitud-1", Unit: "finance", Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"},
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

func TestEvent_PersistsCorrelationIDs(t *testing.T) {
	pool := startTestPool(t)
	repo := postgres.NewRepository()
	ctx := reqctx.WithRequestID(context.Background(), "req-9")

	e := workflow.New(repo, workflow.Config{
		TraceIDs: func(context.Context) (string, string) { return "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7" },
	}, nil, workflow.WithDefinitions(workflow.Definition{
		Name: "purchase", Version: 1,
		Nodes: []workflow.Node{
			{ID: "draft", Start: true, Eligible: workflow.ByPosition("buyer")},
			{ID: "done", Terminal: true},
		},
		Transitions: []workflow.Transition{{From: "draft", To: "done", Action: "submit"}},
	}))

	c, err := e.Open(ctx, pool, workflow.OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "corr-1", Unit: "finance", Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}})
	require.NoError(t, err)
	_, err = e.Move(ctx, pool, workflow.MoveInput{CaseID: c.ID, Action: "submit", Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}})
	require.NoError(t, err)

	history, err := e.History(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, history, 3)
	for _, ev := range history {
		assert.Equal(t, "req-9", ev.RequestID, "seq %d", ev.Seq)
		assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", ev.TraceID, "seq %d", ev.Seq)
		assert.Equal(t, "00f067aa0ba902b7", ev.SpanID, "seq %d", ev.Seq)
	}
}

func TestMigration004_DownRestoresPreviousSchema(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	opts := silentOptions()
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), opts))

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	e := tenantTestEngine(t, postgres.NewRepository())
	c, err := e.Open(ctx, pool, workflow.OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "down-1", Unit: "finance", Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}})
	require.NoError(t, err)

	require.NoError(t, migrate.Down(ctx, connStr, migrations.FS(), opts), "a clean rollback of 004 must succeed")

	var added int
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT count(*) FROM information_schema.columns
        WHERE (table_name = 'workflow_case' AND column_name IN ('tenant', 'last_seq'))
           OR (table_name = 'workflow_event' AND column_name IN ('schema_version', 'detail', 'tenant', 'actor_kind',
               'acting_as', 'on_behalf_of', 'via_client', 'request_id', 'trace_id', 'span_id'))`).Scan(&added))
	assert.Zero(t, added, "Down must drop every column 004 added")

	var constraints int
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT count(*) FROM pg_constraint
        WHERE conrelid = 'workflow_case'::regclass AND conname = 'workflow_case_domain_external_id_key'`).Scan(&constraints))
	assert.Equal(t, 1, constraints, "Down must restore UNIQUE (domain, external_id)")

	var events int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM workflow_event WHERE case_id = $1`, c.ID).Scan(&events))
	assert.Equal(t, 1, events, "rows survive the rollback")
}

func TestMigration004_DownFailsWhileTwoTenantsShareAnExternalID(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	opts := silentOptions()
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), opts))

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	e := tenantTestEngine(t, postgres.NewRepository())
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		_, err := e.Open(ctx, pool, workflow.OpenInput{
			Definition: "purchase", Tenant: tenant, Domain: "compras", ExternalID: "shared-1", Unit: "finance",
			Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"},
		})
		require.NoError(t, err)
	}

	require.Error(t, migrate.Down(ctx, connStr, migrations.FS(), opts),
		"restoring UNIQUE (domain, external_id) must fail while two tenants share one")

	var tenantColumn int
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT count(*) FROM information_schema.columns
        WHERE table_name = 'workflow_case' AND column_name = 'tenant'`).Scan(&tenantColumn))
	assert.Equal(t, 1, tenantColumn, "the failed Down must roll back as a whole, leaving 004 applied")
}

func TestRecordFailure_PersistsWithoutChangingRevision(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()
	e := tenantTestEngine(t, postgres.NewRepository())

	c, err := e.Open(ctx, pool, workflow.OpenInput{Definition: "purchase", Tenant: "tenant-a", Domain: "compras", ExternalID: "fail-1", Unit: "finance", Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}})
	require.NoError(t, err)

	evt, err := e.RecordFailure(ctx, pool, workflow.FailureInput{
		CaseID: c.ID, Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u2"}, Action: "submit",
		Kind: workflow.FailureGuardRejected, Detail: json.RawMessage(`{"guard":"has-budget"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), evt.Seq)

	got, err := postgres.NewRepository().GetByID(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.Revision, got.Revision, "RecordFailure never advances revision")
	assert.Equal(t, int64(2), got.LastSeq)
	assert.Equal(t, "draft", got.State)

	history, err := e.History(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, workflow.EventFailed, history[1].Kind)
	assert.Equal(t, "tenant-a", history[1].Tenant)
	var d workflow.FailureDetail
	require.NoError(t, json.Unmarshal(history[1].Detail, &d))
	assert.Equal(t, workflow.FailureGuardRejected, d.Kind)
	assert.JSONEq(t, `{"guard":"has-budget"}`, string(d.Detail))

	_, err = e.RecordFailure(ctx, pool, workflow.FailureInput{CaseID: uuid.New(), Kind: workflow.FailureConflict})
	require.ErrorIs(t, err, workflow.ErrCaseNotFound)
}

func TestMove_WithStepPersistsStepSubmitted(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()
	e := tenantTestEngine(t, postgres.NewRepository())

	c, err := e.Open(ctx, pool, workflow.OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "step-1", Unit: "finance", Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}})
	require.NoError(t, err)
	step := &workflow.StepRecord{StepID: "draft-form", FormVersion: 2, PayloadDigest: "sha256:beef"}
	_, err = e.Move(ctx, pool, workflow.MoveInput{CaseID: c.ID, Action: "submit", Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}, Step: step})
	require.NoError(t, err)

	history, err := e.History(ctx, pool, c.ID)
	require.NoError(t, err)
	require.Len(t, history, 4, "opened, step_submitted, moved, closed")
	assert.Equal(t, workflow.EventStepSubmitted, history[1].Kind)
	var d workflow.StepDetail
	require.NoError(t, json.Unmarshal(history[1].Detail, &d))
	assert.Equal(t, *step, d)
}

// TestMove_ConcurrentRecordFailureConflictsThenRetries commits a failure
// record between Move's read and its write (from a guard, on a separate pool
// connection): Move's revision CAS still matches, its first append collides
// on the seq the failure took and fails with ErrConflict, the transaction
// rolls back, and a retry from a fresh read succeeds after the failure.
func TestMove_ConcurrentRecordFailureConflictsThenRetries(t *testing.T) {
	pool := startTestPool(t)
	ctx := context.Background()
	e := tenantTestEngine(t, postgres.NewRepository())

	c, err := e.Open(ctx, pool, workflow.OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "race-1", Unit: "finance", Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}})
	require.NoError(t, err)

	guarded := workflow.Definition{
		Name: "purchase", Version: 2,
		Nodes: []workflow.Node{
			{ID: "draft", Start: true, Eligible: workflow.ByPosition("buyer")},
			{ID: "done", Terminal: true},
		},
		Transitions: []workflow.Transition{{From: "draft", To: "done", Action: "submit", Guard: "interleave"}},
	}
	require.NoError(t, e.Register(guarded))
	_, err = pool.Exec(ctx, `UPDATE workflow_case SET version = 2 WHERE id = $1`, c.ID)
	require.NoError(t, err)
	var interleaved bool
	require.NoError(t, e.RegisterGuard("interleave", func(ctx context.Context, _ pgxtx.DBTX, gc workflow.Case) (bool, error) {
		if !interleaved {
			interleaved = true
			if _, err := e.RecordFailure(ctx, pool, workflow.FailureInput{CaseID: gc.ID, Kind: workflow.FailureConflict}); err != nil {
				return false, err
			}
		}
		return true, nil
	}))

	move := func() error {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := e.Move(ctx, tx, workflow.MoveInput{CaseID: c.ID, Action: "submit", Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: "u1"}}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	require.ErrorIs(t, move(), workflow.ErrConflict)
	require.NoError(t, move(), "a retry from a fresh read must succeed")

	history, err := e.History(ctx, pool, c.ID)
	require.NoError(t, err)
	kinds := make([]workflow.EventKind, len(history))
	for i, ev := range history {
		kinds[i] = ev.Kind
		assert.Equal(t, int64(i+1), ev.Seq)
	}
	assert.Equal(t, []workflow.EventKind{workflow.EventOpened, workflow.EventFailed, workflow.EventMoved, workflow.EventClosed}, kinds)
}
