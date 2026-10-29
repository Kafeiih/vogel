//go:build integration

package process_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/workflow"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/application/process"
	domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/bpms"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/bpms/bpmstest"
)

type app struct {
	t   *testing.T
	ctx context.Context
	dir *bpms.MemoryDirectory
	d   *bpms.Domain
	db  process.DB
	eng *workflow.Engine
}

func newApp(t *testing.T) *app {
	t.Helper()
	ctx := context.Background()
	pool := bpmstest.NewPool(t)
	dir := bpms.NewMemoryDirectory(bpms.ProfileHolder{Position: "encargado", Unit: "atencion", UserID: "luis"})
	eng := bpmstest.NewEngine()
	d, err := bpms.NewDomain(ctx, pool, eng, dir)
	require.NoError(t, err)
	return &app{t: t, ctx: ctx, dir: dir, d: d, db: pool, eng: eng}
}

func validConsulta() map[string]any {
	return map[string]any{"nombre": "Ana", "correo": "ana@example.cl", "tema": "general", "consulta": "¿Horario de atención?"}
}

func (a *app) startCase() uuid.UUID {
	a.t.Helper()
	c, err := a.d.Service.Start(a.ctx, "ana", "consulta", "enviar", validConsulta())
	require.NoError(a.t, err)
	return c.ID
}

func (a *app) inbox(actor string) []uuid.UUID {
	a.t.Helper()
	cases, err := a.d.Service.Inbox(a.ctx, actor)
	require.NoError(a.t, err)
	ids := make([]uuid.UUID, 0, len(cases))
	for i := range cases {
		ids = append(ids, cases[i].ID)
	}
	return ids
}

func answer() map[string]any { return map[string]any{"respuesta": "De 9 a 14 h."} }

func TestStart_MovesTheCaseToTheEncargado(t *testing.T) {
	a := newApp(t)

	c, err := a.d.Service.Start(a.ctx, "ana", "consulta", "enviar", validConsulta())

	require.NoError(t, err)
	assert.Equal(t, "respuesta", c.State)
	assert.Equal(t, 1, c.Version)
}

func TestStart_WithInvalidDataCreatesNothing(t *testing.T) {
	a := newApp(t)
	values := validConsulta()
	values["tema"] = "reclamo"
	values["correo"] = "ana"

	_, err := a.d.Service.Start(a.ctx, "ana", "consulta", "enviar", values)

	var formErr *domain.FormError
	require.ErrorAs(t, err, &formErr)
	assert.Equal(t, "must be a single email address", formErr.Fields["correo"])
	assert.Equal(t, "is required", formErr.Fields["numero_atencion"])
	assert.Empty(t, a.inbox("luis"))
}

func TestStart_UnknownProcess(t *testing.T) {
	a := newApp(t)
	_, err := a.d.Service.Start(a.ctx, "ana", "nope", "enviar", validConsulta())
	assert.ErrorIs(t, err, domain.ErrProcessNotFound)
}

func TestInbox_FollowsTheProfileHolderLive(t *testing.T) {
	a := newApp(t)
	id := a.startCase()

	assert.Equal(t, []uuid.UUID{id}, a.inbox("luis"))
	assert.Empty(t, a.inbox("maria"))

	a.dir.Set("encargado", "atencion", "maria")
	assert.Equal(t, []uuid.UUID{id}, a.inbox("maria"))
	assert.Empty(t, a.inbox("luis"))
}

func TestClaim_OnlyTheCurrentHolder(t *testing.T) {
	a := newApp(t)
	id := a.startCase()
	a.dir.Set("encargado", "atencion", "maria")

	_, err := a.d.Service.Claim(a.ctx, "luis", id)
	assert.ErrorIs(t, err, domain.ErrNotHolder)

	c, err := a.d.Service.Claim(a.ctx, "maria", id)
	require.NoError(t, err)
	assert.Equal(t, "maria", c.AssignedTo)
}

func TestClaim_TheClaimantKeepsTheTaskWhenTheProfileChanges(t *testing.T) {
	a := newApp(t)
	id := a.startCase()
	_, err := a.d.Service.Claim(a.ctx, "luis", id)
	require.NoError(t, err)

	a.dir.Set("encargado", "atencion", "maria")

	assert.Equal(t, []uuid.UUID{id}, a.inbox("luis"))
	assert.Empty(t, a.inbox("maria"))
}

func TestSubmit_OnlyTheClaimant(t *testing.T) {
	a := newApp(t)
	id := a.startCase()

	_, err := a.d.Service.Submit(a.ctx, "luis", id, "responder", answer())
	assert.ErrorIs(t, err, domain.ErrNotClaimant, "an unclaimed case cannot be submitted")

	_, err = a.d.Service.Claim(a.ctx, "luis", id)
	require.NoError(t, err)
	_, err = a.d.Service.Submit(a.ctx, "maria", id, "responder", answer())
	assert.ErrorIs(t, err, domain.ErrNotClaimant)

	_, err = a.d.Service.Submit(a.ctx, "luis", id, "responder", map[string]any{})
	var formErr *domain.FormError
	require.ErrorAs(t, err, &formErr)
	assert.Equal(t, "is required", formErr.Fields["respuesta"])

	c, err := a.d.Service.Submit(a.ctx, "luis", id, "responder", answer())
	require.NoError(t, err)
	assert.Equal(t, "cerrada", c.State)
	assert.Equal(t, workflow.StatusClosed, c.Status)

	_, err = a.d.Service.Submit(a.ctx, "luis", id, "responder", answer())
	assert.ErrorIs(t, err, workflow.ErrCaseClosed)
}

func TestSubmit_RejectedMoveIsRecorded(t *testing.T) {
	a := newApp(t)
	id := a.startCase()
	_, err := a.d.Service.Claim(a.ctx, "luis", id)
	require.NoError(t, err)
	before, err := a.eng.History(a.ctx, a.db, id)
	require.NoError(t, err)

	_, err = a.d.Service.Submit(a.ctx, "luis", id, "archivar", answer())
	require.ErrorIs(t, err, workflow.ErrInvalidTransition)

	history, err := a.eng.History(a.ctx, a.db, id)
	require.NoError(t, err)
	require.Len(t, history, len(before)+1, "the rejected submit leaves one failure record")
	failed := history[len(history)-1]
	assert.Equal(t, workflow.EventFailed, failed.Kind)
	assert.Equal(t, "archivar", failed.Action)
	assert.Equal(t, workflow.Actor{Kind: workflow.ActorHuman, ID: "luis"}, failed.Actor)
	var detail workflow.FailureDetail
	require.NoError(t, json.Unmarshal(failed.Detail, &detail))
	assert.Equal(t, workflow.FailureTransitionNotFound, detail.Kind)

	c, err := a.d.Service.Submit(a.ctx, "luis", id, "responder", answer())
	require.NoError(t, err, "the case is unchanged and still accepts the right action")
	assert.Equal(t, "cerrada", c.State)
}

func TestSubmit_FormErrorIsNotRecorded(t *testing.T) {
	a := newApp(t)
	id := a.startCase()
	_, err := a.d.Service.Claim(a.ctx, "luis", id)
	require.NoError(t, err)
	before, err := a.eng.History(a.ctx, a.db, id)
	require.NoError(t, err)

	_, err = a.d.Service.Submit(a.ctx, "luis", id, "responder", map[string]any{})
	var formErr *domain.FormError
	require.ErrorAs(t, err, &formErr)

	history, err := a.eng.History(a.ctx, a.db, id)
	require.NoError(t, err)
	assert.Len(t, history, len(before), "invalid form data is not a rejected move")
}

func TestView_ShowsValuesAndHistoryToRelatedActors(t *testing.T) {
	a := newApp(t)
	id := a.startCase()
	_, err := a.d.Service.Claim(a.ctx, "luis", id)
	require.NoError(t, err)
	_, err = a.d.Service.Submit(a.ctx, "luis", id, "responder", answer())
	require.NoError(t, err)

	v, err := a.d.Service.View(a.ctx, "ana", id)
	require.NoError(t, err)
	assert.Equal(t, "De 9 a 14 h.", v.Values["respuesta"])
	last := v.History[len(v.History)-1]
	assert.Equal(t, "respuesta", last.NodeID)
	assert.Equal(t, "luis", last.ActorID)

	_, err = a.d.Service.View(a.ctx, "luis", id)
	assert.NoError(t, err, "whoever worked the case keeps access after it closes")

	_, err = a.d.Service.View(a.ctx, "pedro", id)
	assert.ErrorIs(t, err, domain.ErrNoAccess)
}

func TestView_TheCurrentHolderSeesAnOpenCase(t *testing.T) {
	a := newApp(t)
	id := a.startCase()

	_, err := a.d.Service.View(a.ctx, "luis", id)
	assert.NoError(t, err)
}

func TestCatalog_AnOpenCaseKeepsItsVersion(t *testing.T) {
	a := newApp(t)
	id := a.startCase()

	latest, ok := a.d.Catalog.Latest("consulta")
	require.True(t, ok)
	spec := latest.Spec
	spec.Nodes = append([]domain.NodeSpec(nil), spec.Nodes...)
	spec.Nodes[1].Form = append(append([]domain.FieldSpec(nil), spec.Nodes[1].Form...),
		domain.FieldSpec{Key: "folio", Type: "text", Label: "Folio", Required: true})
	p, err := a.d.Catalog.Publish(a.ctx, a.db, spec)
	require.NoError(t, err)
	assert.Equal(t, 2, p.Definition.Version)

	_, err = a.d.Service.Claim(a.ctx, "luis", id)
	require.NoError(t, err)
	_, err = a.d.Service.Submit(a.ctx, "luis", id, "responder", answer())
	assert.NoError(t, err, "version 1 does not require folio")

	c, err := a.d.Service.Start(a.ctx, "ana", "consulta", "enviar", validConsulta())
	require.NoError(t, err)
	assert.Equal(t, 2, c.Version)
}

func TestCatalog_PublishStoresNothingForAnInvalidSpec(t *testing.T) {
	a := newApp(t)
	latest, _ := a.d.Catalog.Latest("consulta")
	spec := latest.Spec
	spec.Nodes = append([]domain.NodeSpec(nil), spec.Nodes...)
	spec.Nodes[0].Form = append([]domain.FieldSpec(nil), spec.Nodes[0].Form...)
	spec.Nodes[0].Form[0].Type = "rut"

	_, err := a.d.Service.Publish(a.ctx, spec)

	var compileErr *domain.CompileError
	require.ErrorAs(t, err, &compileErr)
	_, ok := a.d.Catalog.Get("consulta", 2)
	assert.False(t, ok)
	var n int
	require.NoError(t, a.db.QueryRow(a.ctx, `SELECT count(*) FROM process_version`).Scan(&n))
	assert.Equal(t, 1, n)
}
