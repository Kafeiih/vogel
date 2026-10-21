//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/workflow"
	wfpg "github.com/kafeiih/vogel/workflow/postgres"
)

type testApp struct {
	t       *testing.T
	server  *httptest.Server
	safo    *FakeSAFO
	catalog *Catalog
}

// newTestApp wires the same composition main.go builds, on a fresh database,
// with "luis" as the titular encargado of the "atencion" unit.
func newTestApp(t *testing.T) *testApp {
	t.Helper()
	ctx := context.Background()
	pool := newTestPool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	reg := testRegistry(t)
	repo := wfpg.NewRepository()
	engine := workflow.New(repo, workflow.Config{}, logger)
	catalog := NewCatalog(reg, engine)
	require.NoError(t, catalog.Load(ctx, pool))
	_, err := catalog.Publish(ctx, pool, loadConsultaSpec(t))
	require.NoError(t, err)

	safo := NewFakeSAFO()
	safo.SetTitular("encargado", "atencion", "luis")

	svc := NewService(pool, engine, repo, catalog, safo)
	server := httptest.NewServer(NewRouter(svc, catalog, safo, pool, logger))
	t.Cleanup(server.Close)
	return &testApp{t: t, server: server, safo: safo, catalog: catalog}
}

func (a *testApp) do(method, path, actor string, body any) (int, map[string]any) {
	a.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(a.t, json.NewEncoder(&buf).Encode(body))
	}
	req, err := http.NewRequest(method, a.server.URL+path, &buf)
	require.NoError(a.t, err)
	if actor != "" {
		req.Header.Set(actorHeader, actor)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(a.t, err)
	defer res.Body.Close()

	var out map[string]any
	raw, err := io.ReadAll(res.Body)
	require.NoError(a.t, err)
	if len(raw) > 0 {
		require.NoError(a.t, json.Unmarshal(raw, &out), string(raw))
	}
	return res.StatusCode, out
}

func validConsulta() map[string]any {
	return map[string]any{
		"action": "enviar",
		"values": map[string]any{
			"nombre": "Ana", "correo": "ana@example.cl", "tema": "general", "consulta": "¿Horario de atención?",
		},
	}
}

func (a *testApp) startCase() string {
	a.t.Helper()
	status, body := a.do(http.MethodPost, "/processes/consulta/cases", "ana", validConsulta())
	require.Equal(a.t, http.StatusCreated, status, body)
	data := body["data"].(map[string]any)
	return data["id"].(string)
}

func inboxIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	var ids []string
	for _, item := range body["data"].([]any) {
		ids = append(ids, item.(map[string]any)["id"].(string))
	}
	return ids
}

func TestStartingACaseMovesItToTheEncargado(t *testing.T) {
	app := newTestApp(t)

	status, body := app.do(http.MethodPost, "/processes/consulta/cases", "ana", validConsulta())

	require.Equal(t, http.StatusCreated, status, body)
	data := body["data"].(map[string]any)
	assert.Equal(t, "respuesta", data["state"])
	assert.Equal(t, float64(1), data["version"])
}

func TestStartingACaseWithInvalidDataCreatesNothing(t *testing.T) {
	app := newTestApp(t)
	req := validConsulta()
	req["values"].(map[string]any)["tema"] = "reclamo"
	req["values"].(map[string]any)["correo"] = "ana"

	status, body := app.do(http.MethodPost, "/processes/consulta/cases", "ana", req)

	assert.Equal(t, http.StatusBadRequest, status, body)
	assert.Equal(t, map[string]any{
		"correo":          "must be a single email address",
		"numero_atencion": "is required",
	}, body["fields"])
	_, inbox := app.do(http.MethodGet, "/inbox", "luis", nil)
	assert.Empty(t, inboxIDs(t, inbox))
}

func TestRequestsWithoutAnActorAreRejected(t *testing.T) {
	app := newTestApp(t)

	status, _ := app.do(http.MethodGet, "/inbox", "", nil)

	assert.Equal(t, http.StatusUnauthorized, status)
}

func TestInboxFollowsTheProfileHolderLive(t *testing.T) {
	app := newTestApp(t)
	id := app.startCase()

	_, body := app.do(http.MethodGet, "/inbox", "luis", nil)
	assert.Equal(t, []string{id}, inboxIDs(t, body))
	_, body = app.do(http.MethodGet, "/inbox", "maria", nil)
	assert.Empty(t, inboxIDs(t, body))

	// A subrogation starts: the same pending task now belongs to maria,
	// with nothing reassigned.
	app.safo.SetSubrogante("encargado", "atencion", "maria")

	_, body = app.do(http.MethodGet, "/inbox", "maria", nil)
	assert.Equal(t, []string{id}, inboxIDs(t, body))
	_, body = app.do(http.MethodGet, "/inbox", "luis", nil)
	assert.Empty(t, inboxIDs(t, body))

	app.safo.EndSubrogation("encargado", "atencion")

	_, body = app.do(http.MethodGet, "/inbox", "luis", nil)
	assert.Equal(t, []string{id}, inboxIDs(t, body))
}

func TestOnlyTheCurrentHolderCanClaim(t *testing.T) {
	app := newTestApp(t)
	id := app.startCase()
	app.safo.SetSubrogante("encargado", "atencion", "maria")

	status, _ := app.do(http.MethodPost, "/cases/"+id+"/claim", "luis", nil)
	assert.Equal(t, http.StatusForbidden, status)

	status, body := app.do(http.MethodPost, "/cases/"+id+"/claim", "maria", nil)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "maria", body["data"].(map[string]any)["assignedTo"])
}

func TestTheClaimantKeepsTheTaskWhenTheProfileChanges(t *testing.T) {
	app := newTestApp(t)
	id := app.startCase()
	status, _ := app.do(http.MethodPost, "/cases/"+id+"/claim", "luis", nil)
	require.Equal(t, http.StatusOK, status)

	app.safo.SetSubrogante("encargado", "atencion", "maria")

	_, body := app.do(http.MethodGet, "/inbox", "luis", nil)
	assert.Equal(t, []string{id}, inboxIDs(t, body))
	_, body = app.do(http.MethodGet, "/inbox", "maria", nil)
	assert.Empty(t, inboxIDs(t, body))
}

func TestOnlyTheClaimantCanSubmit(t *testing.T) {
	app := newTestApp(t)
	id := app.startCase()
	answer := map[string]any{"action": "responder", "values": map[string]any{"respuesta": "De 9 a 14 h."}}

	status, _ := app.do(http.MethodPost, "/cases/"+id+"/submit", "luis", answer)
	assert.Equal(t, http.StatusForbidden, status, "an unclaimed case cannot be submitted")

	status, _ = app.do(http.MethodPost, "/cases/"+id+"/claim", "luis", nil)
	require.Equal(t, http.StatusOK, status)
	status, _ = app.do(http.MethodPost, "/cases/"+id+"/submit", "maria", answer)
	assert.Equal(t, http.StatusForbidden, status)

	status, body := app.do(http.MethodPost, "/cases/"+id+"/submit", "luis",
		map[string]any{"action": "responder", "values": map[string]any{}})
	assert.Equal(t, http.StatusBadRequest, status, body)
	assert.Equal(t, map[string]any{"respuesta": "is required"}, body["fields"])

	status, body = app.do(http.MethodPost, "/cases/"+id+"/submit", "luis", answer)
	require.Equal(t, http.StatusOK, status, body)
	data := body["data"].(map[string]any)
	assert.Equal(t, "cerrada", data["state"])
	assert.Equal(t, "closed", data["status"])
}

func TestCaseViewShowsValuesAndTheirHistory(t *testing.T) {
	app := newTestApp(t)
	id := app.startCase()
	app.do(http.MethodPost, "/cases/"+id+"/claim", "luis", nil)
	app.do(http.MethodPost, "/cases/"+id+"/submit", "luis",
		map[string]any{"action": "responder", "values": map[string]any{"respuesta": "De 9 a 14 h."}})

	status, body := app.do(http.MethodGet, "/cases/"+id, "ana", nil)

	require.Equal(t, http.StatusOK, status, body)
	data := body["data"].(map[string]any)
	assert.Equal(t, "De 9 a 14 h.", data["values"].(map[string]any)["respuesta"])
	history := data["history"].([]any)
	last := history[len(history)-1].(map[string]any)
	assert.Equal(t, "respuesta", last["nodeId"])
	assert.Equal(t, "luis", last["actorId"])

	status, _ = app.do(http.MethodGet, "/cases/"+id, "pedro", nil)
	assert.Equal(t, http.StatusForbidden, status)
}

func TestAnOpenCaseKeepsItsVersion(t *testing.T) {
	app := newTestApp(t)
	id := app.startCase()

	// Version 2 adds a required field to the answer form.
	spec := loadConsultaSpec(t)
	spec.Nodes[1].Form = append(spec.Nodes[1].Form, FieldSpec{Key: "folio", Type: "text", Label: "Folio", Required: true})
	status, body := app.do(http.MethodPost, "/processes", "admin", spec)
	require.Equal(t, http.StatusCreated, status, body)
	assert.Equal(t, float64(2), body["data"].(map[string]any)["version"])

	app.do(http.MethodPost, "/cases/"+id+"/claim", "luis", nil)
	status, body = app.do(http.MethodPost, "/cases/"+id+"/submit", "luis",
		map[string]any{"action": "responder", "values": map[string]any{"respuesta": "De 9 a 14 h."}})
	assert.Equal(t, http.StatusOK, status, body)

	status, body = app.do(http.MethodPost, "/processes/consulta/cases", "ana", validConsulta())
	require.Equal(t, http.StatusCreated, status, body)
	assert.Equal(t, float64(2), body["data"].(map[string]any)["version"])
}

func TestPublishingAnInvalidSpecIsRejected(t *testing.T) {
	app := newTestApp(t)
	spec := loadConsultaSpec(t)
	spec.Nodes[0].Form[0].Type = "rut"

	status, body := app.do(http.MethodPost, "/processes", "admin", spec)

	assert.Equal(t, http.StatusUnprocessableEntity, status, body)
	_, ok := app.catalog.Get("consulta", 2)
	assert.False(t, ok)
}
