package process_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/application/process"
	domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
)

// These tests pin which Submit errors leave a failure record, without a
// database: memStore keeps the committed state, and a transaction that rolls
// back restores the snapshot it took at Begin, as Postgres would.

var errNoSQL = errors.New("memory store runs no SQL")

type memStore struct {
	cases  map[uuid.UUID]workflow.Case
	events map[uuid.UUID][]workflow.Event
	values map[uuid.UUID]map[string]any
}

func newMemStore() *memStore {
	return &memStore{
		cases:  map[uuid.UUID]workflow.Case{},
		events: map[uuid.UUID][]workflow.Event{},
		values: map[uuid.UUID]map[string]any{},
	}
}

func (s *memStore) clone() memStore {
	out := memStore{
		cases:  maps.Clone(s.cases),
		events: make(map[uuid.UUID][]workflow.Event, len(s.events)),
		values: make(map[uuid.UUID]map[string]any, len(s.values)),
	}
	for id, evs := range s.events {
		out.events[id] = slices.Clone(evs)
	}
	for id, v := range s.values {
		out.values[id] = maps.Clone(v)
	}
	return out
}

type memDB struct{ st *memStore }

func (d *memDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errNoSQL
}

func (d *memDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, errNoSQL }

func (d *memDB) QueryRow(context.Context, string, ...any) pgx.Row { return errRow{} }

func (d *memDB) Begin(context.Context) (pgx.Tx, error) {
	return &memTx{db: d, snapshot: d.st.clone()}, nil
}

type errRow struct{}

func (errRow) Scan(...any) error { return errNoSQL }

// memTx embeds pgx.Tx only to satisfy the interface; the code under test
// reaches the store through the fakes, never through the transaction.
type memTx struct {
	pgx.Tx
	db       *memDB
	snapshot memStore
	done     bool
}

func (t *memTx) Commit(context.Context) error {
	if t.done {
		return pgx.ErrTxClosed
	}
	t.done = true
	return nil
}

func (t *memTx) Rollback(context.Context) error {
	if t.done {
		return pgx.ErrTxClosed
	}
	t.done = true
	*t.db.st = t.snapshot
	return nil
}

// memCases is the workflow.Repository Submit's engine and case reader use.
type memCases struct {
	st        *memStore
	updateErr error // returned by Update when set
}

func (r *memCases) Create(_ context.Context, _ pgxtx.DBTX, c *workflow.Case) error {
	c.Revision = 0
	r.st.cases[c.ID] = *c
	return nil
}

func (r *memCases) GetByID(_ context.Context, _ pgxtx.DBTX, id uuid.UUID) (*workflow.Case, error) {
	c, ok := r.st.cases[id]
	if !ok {
		return nil, workflow.ErrCaseNotFound
	}
	return &c, nil
}

func (r *memCases) GetByExternalID(context.Context, pgxtx.DBTX, string, string, string) (*workflow.Case, error) {
	return nil, workflow.ErrCaseNotFound
}

func (r *memCases) Update(_ context.Context, _ pgxtx.DBTX, c *workflow.Case, expectedLastSeq int64) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	stored, ok := r.st.cases[c.ID]
	if !ok {
		return workflow.ErrCaseNotFound
	}
	if stored.Revision != c.Revision || stored.LastSeq != expectedLastSeq {
		return workflow.ErrConflict
	}
	c.Revision++
	r.st.cases[c.ID] = *c
	return nil
}

func (r *memCases) AppendEvent(_ context.Context, _ pgxtx.DBTX, e *workflow.Event) error {
	r.st.events[e.CaseID] = append(r.st.events[e.CaseID], *e)
	return nil
}

func (r *memCases) ListEvents(_ context.Context, _ pgxtx.DBTX, caseID uuid.UUID) ([]workflow.Event, error) {
	return slices.Clone(r.st.events[caseID]), nil
}

func (r *memCases) AdvanceLastSeq(_ context.Context, _ pgxtx.DBTX, id uuid.UUID) (*workflow.Case, error) {
	c, ok := r.st.cases[id]
	if !ok {
		return nil, workflow.ErrCaseNotFound
	}
	c.LastSeq++
	r.st.cases[id] = c
	return &c, nil
}

func (r *memCases) ListByEligibility(context.Context, pgxtx.DBTX, workflow.InboxFilter) ([]workflow.Case, error) {
	return nil, errNoSQL
}

func (r *memCases) ListParticipated(context.Context, pgxtx.DBTX, workflow.ParticipationFilter) ([]workflow.Case, error) {
	return nil, errNoSQL
}

func (r *memCases) ListEventDigests(context.Context, pgxtx.DBTX, uuid.UUID) ([]workflow.EventDigest, error) {
	return nil, errNoSQL
}

type memValues struct{ st *memStore }

func (v *memValues) Save(_ context.Context, _ pgxtx.DBTX, caseID uuid.UUID, _, _ string, values map[string]any) error {
	if v.st.values[caseID] == nil {
		v.st.values[caseID] = map[string]any{}
	}
	maps.Copy(v.st.values[caseID], values)
	return nil
}

func (v *memValues) Current(_ context.Context, _ pgxtx.DBTX, caseID uuid.UUID) (map[string]any, error) {
	return maps.Clone(v.st.values[caseID]), nil
}

func (v *memValues) History(context.Context, pgxtx.DBTX, uuid.UUID) ([]domain.CaseValue, error) {
	return nil, nil
}

type memVersions struct{}

func (memVersions) Insert(context.Context, pgxtx.DBTX, string, []byte) (int, error) { return 1, nil }

func (memVersions) InsertFirst(context.Context, pgxtx.DBTX, string, []byte) (bool, error) {
	return true, nil
}

func (memVersions) All(context.Context, pgxtx.DBTX) ([]process.StoredVersion, error) { return nil, nil }

const consultaSpec = `{
  "name": "consulta",
  "nodes": [
    {"id": "ingreso", "start": true, "form": [
      {"key": "consulta", "type": "textarea", "label": "Consulta", "required": true}
    ]},
    {"id": "respuesta", "profile": {"position": "encargado", "unit": "atencion"}, "form": [
      {"key": "respuesta", "type": "textarea", "label": "Respuesta", "required": true}
    ]},
    {"id": "cerrada", "terminal": true}
  ],
  "transitions": [
    {"from": "ingreso", "to": "respuesta", "action": "enviar"},
    {"from": "respuesta", "to": "cerrada", "action": "responder"}
  ]
}`

type submitFixture struct {
	t     *testing.T
	ctx   context.Context
	st    *memStore
	db    *memDB
	cases *memCases
	eng   *workflow.Engine
	svc   *process.Service
}

func newSubmitFixture(t *testing.T) *submitFixture {
	t.Helper()
	ctx := context.Background()
	st := newMemStore()
	db := &memDB{st: st}
	cases := &memCases{st: st}
	eng := workflow.New(cases, workflow.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fields, err := domain.NewFieldRegistry(domain.DefaultFieldTypes()...)
	require.NoError(t, err)
	catalog := process.NewCatalog(fields, eng, memVersions{})
	require.NoError(t, catalog.EnsurePublished(ctx, db, []byte(consultaSpec)))
	svc := process.NewService(process.ServiceDeps{DB: db, Engine: eng, Cases: cases, Values: &memValues{st: st}, Catalog: catalog})
	return &submitFixture{t: t, ctx: ctx, st: st, db: db, cases: cases, eng: eng, svc: svc}
}

// claimedCase opens a case, sends it to "respuesta" and claims it for luis.
func (f *submitFixture) claimedCase() uuid.UUID {
	f.t.Helper()
	c, err := f.svc.Start(f.ctx, "ana", "consulta", "enviar", map[string]any{"consulta": "¿Horario?"})
	require.NoError(f.t, err)
	require.NoError(f.t, pgx.BeginFunc(f.ctx, f.db, func(tx pgx.Tx) error {
		_, err := f.eng.Claim(f.ctx, tx, c.ID, "luis")
		return err
	}))
	return c.ID
}

func (f *submitFixture) history(id uuid.UUID) []workflow.Event {
	f.t.Helper()
	h, err := f.eng.History(f.ctx, f.db, id)
	require.NoError(f.t, err)
	return h
}

func TestSubmitUnit_RejectedMoveIsRecordedAndItsValuesRolledBack(t *testing.T) {
	f := newSubmitFixture(t)
	id := f.claimedCase()
	before := f.history(id)
	stateBefore := f.st.cases[id].State

	c, err := f.svc.Submit(f.ctx, "luis", id, "archivar", map[string]any{"respuesta": "De 9 a 14 h."})
	require.ErrorIs(t, err, workflow.ErrInvalidTransition)
	assert.Nil(t, c)

	history := f.history(id)
	require.Len(t, history, len(before)+1, "the rejected submit leaves one failure record")
	failed := history[len(history)-1]
	assert.Equal(t, workflow.EventFailed, failed.Kind)
	assert.Equal(t, "archivar", failed.Action)
	assert.Equal(t, workflow.Actor{Kind: workflow.ActorHuman, ID: "luis"}, failed.Actor)
	var detail workflow.FailureDetail
	require.NoError(t, json.Unmarshal(failed.Detail, &detail))
	assert.Equal(t, workflow.FailureTransitionNotFound, detail.Kind)

	assert.Equal(t, stateBefore, f.st.cases[id].State, "the case did not move")
	assert.NotContains(t, f.st.values[id], "respuesta", "the form values went back with the rejected move")
}

func TestSubmitUnit_ErrorsThatAreNotRejectedMovesAreNotRecorded(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(f *submitFixture, id uuid.UUID)
		actor   string
		values  map[string]any
		wantErr error
	}{
		{
			name:    "not the claimant",
			actor:   "maria",
			values:  map[string]any{"respuesta": "De 9 a 14 h."},
			wantErr: domain.ErrNotClaimant,
		},
		{
			name: "closed case",
			arrange: func(f *submitFixture, id uuid.UUID) {
				_, err := f.svc.Submit(f.ctx, "luis", id, "responder", map[string]any{"respuesta": "De 9 a 14 h."})
				require.NoError(f.t, err)
			},
			actor:   "luis",
			values:  map[string]any{"respuesta": "Otra."},
			wantErr: workflow.ErrCaseClosed,
		},
		{
			name:   "invalid form",
			actor:  "luis",
			values: map[string]any{},
		},
		{
			name: "database failure inside the move",
			arrange: func(f *submitFixture, _ uuid.UUID) {
				f.cases.updateErr = errNoSQL
			},
			actor:   "luis",
			values:  map[string]any{"respuesta": "De 9 a 14 h."},
			wantErr: errNoSQL,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSubmitFixture(t)
			id := f.claimedCase()
			if tt.arrange != nil {
				tt.arrange(f, id)
			}
			before := f.history(id)

			_, err := f.svc.Submit(f.ctx, tt.actor, id, "responder", tt.values)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				var formErr *domain.FormError
				require.ErrorAs(t, err, &formErr)
			}
			_, isFailure := workflow.FailureKindOf(err)
			require.False(t, isFailure, "the case must not be a rejected move")
			assert.Len(t, f.history(id), len(before), "no failure record")
		})
	}
}

func TestSubmitUnit_AcceptedMoveLeavesNoFailureRecord(t *testing.T) {
	f := newSubmitFixture(t)
	id := f.claimedCase()
	before := f.history(id)

	c, err := f.svc.Submit(f.ctx, "luis", id, "responder", map[string]any{"respuesta": "De 9 a 14 h."})
	require.NoError(t, err)
	assert.Equal(t, "cerrada", c.State)

	added := f.history(id)[len(before):]
	kinds := make([]workflow.EventKind, 0, len(added))
	for _, ev := range added {
		kinds = append(kinds, ev.Kind)
	}
	assert.Equal(t, []workflow.EventKind{workflow.EventMoved, workflow.EventClosed}, kinds, "the move to the terminal node, no failure record")
	assert.Equal(t, "De 9 a 14 h.", f.st.values[id]["respuesta"])
}
