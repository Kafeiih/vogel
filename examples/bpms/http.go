package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/workflow"
)

// actorHeader identifies the caller. This example has no authentication: a
// real deployment takes the actor from the authenticated principal, as
// examples/api does with its auth middleware.
const actorHeader = "X-Actor"

// maxBodyBytes bounds every request body.
const maxBodyBytes = 1 << 20

type submission struct {
	Action string         `json:"action"`
	Values map[string]any `json:"values"`
}

// caseDTO is a case as this API returns it. workflow.Case has no JSON tags
// on purpose: the wire shape is the consumer's choice.
type caseDTO struct {
	ID         uuid.UUID  `json:"id"`
	Process    string     `json:"process"`
	Version    int        `json:"version"`
	State      string     `json:"state"`
	Status     string     `json:"status"`
	AssignedTo string     `json:"assignedTo,omitempty"`
	OpenedAt   time.Time  `json:"openedAt"`
	ClosedAt   *time.Time `json:"closedAt,omitempty"`
}

func toCaseDTO(c *workflow.Case) caseDTO {
	return caseDTO{
		ID: c.ID, Process: c.Definition, Version: c.Version, State: c.State,
		Status: string(c.Status), AssignedTo: c.AssignedTo, OpenedAt: c.OpenedAt, ClosedAt: c.ClosedAt,
	}
}

type caseViewDTO struct {
	caseDTO
	Values  map[string]any `json:"values"`
	History []CaseValue    `json:"history"`
}

type subrogation struct {
	Position string `json:"position"`
	Unit     string `json:"unit"`
	Actor    string `json:"actor"`
}

type handler struct {
	svc     *Service
	catalog *Catalog
	safo    *FakeSAFO
	pool    *pgxpool.Pool
	logger  *slog.Logger
}

// NewRouter exposes the example's HTTP API. safo is the fake directory;
// its two routes exist only to drive a subrogation by hand.
func NewRouter(svc *Service, catalog *Catalog, safo *FakeSAFO, pool *pgxpool.Pool, logger *slog.Logger) http.Handler {
	h := &handler{svc: svc, catalog: catalog, safo: safo, pool: pool, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /processes", h.withActor(h.publish))
	mux.HandleFunc("GET /processes/{name}", h.withActor(h.latest))
	mux.HandleFunc("POST /processes/{name}/cases", h.withActor(h.start))
	mux.HandleFunc("GET /inbox", h.withActor(h.inbox))
	mux.HandleFunc("GET /cases/{id}", h.withActor(h.view))
	mux.HandleFunc("POST /cases/{id}/claim", h.withActor(h.claim))
	mux.HandleFunc("POST /cases/{id}/submit", h.withActor(h.submit))
	mux.HandleFunc("PUT /fake-safo/subrogation", h.withActor(h.startSubrogation))
	mux.HandleFunc("DELETE /fake-safo/subrogation", h.withActor(h.endSubrogation))
	return mux
}

type actorHandler func(w http.ResponseWriter, r *http.Request, actor string)

func (h *handler) withActor(next actorHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := r.Header.Get(actorHeader)
		if actor == "" {
			response.Error(w, r, http.StatusUnauthorized, response.CodeUnauthorized, "missing "+actorHeader+" header")
			return
		}
		next(w, r, actor)
	}
}

func (h *handler) publish(w http.ResponseWriter, r *http.Request, _ string) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON, "could not read body")
		return
	}
	spec, err := ParseProcessSpec(raw)
	if err != nil {
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON, err.Error())
		return
	}
	p, err := h.catalog.Publish(r.Context(), h.pool, spec)
	if err != nil {
		// Every compile error is the author's to fix, so it is safe and
		// useful to return as is.
		response.Error(w, r, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity, err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, map[string]any{"name": p.Definition.Name, "version": p.Definition.Version})
}

func (h *handler) latest(w http.ResponseWriter, r *http.Request, _ string) {
	p, ok := h.catalog.Latest(r.PathValue("name"))
	if !ok {
		response.Error(w, r, http.StatusNotFound, response.CodeNotFound, errProcessNotFound.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"version": p.Definition.Version, "spec": p.Spec})
}

func (h *handler) start(w http.ResponseWriter, r *http.Request, actor string) {
	var in submission
	if !decode(w, r, &in) {
		return
	}
	c, err := h.svc.Start(r.Context(), actor, r.PathValue("name"), in.Action, in.Values)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusCreated, toCaseDTO(c))
}

func (h *handler) inbox(w http.ResponseWriter, r *http.Request, actor string) {
	cases, err := h.svc.Inbox(r.Context(), actor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]caseDTO, 0, len(cases))
	for i := range cases {
		out = append(out, toCaseDTO(&cases[i]))
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *handler) view(w http.ResponseWriter, r *http.Request, actor string) {
	id, ok := caseID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.View(r.Context(), actor, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, caseViewDTO{caseDTO: toCaseDTO(v.Case), Values: v.Values, History: v.History})
}

func (h *handler) claim(w http.ResponseWriter, r *http.Request, actor string) {
	id, ok := caseID(w, r)
	if !ok {
		return
	}
	c, err := h.svc.Claim(r.Context(), actor, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toCaseDTO(c))
}

func (h *handler) submit(w http.ResponseWriter, r *http.Request, actor string) {
	id, ok := caseID(w, r)
	if !ok {
		return
	}
	var in submission
	if !decode(w, r, &in) {
		return
	}
	c, err := h.svc.Submit(r.Context(), actor, id, in.Action, in.Values)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toCaseDTO(c))
}

func (h *handler) startSubrogation(w http.ResponseWriter, r *http.Request, _ string) {
	var in subrogation
	if !decode(w, r, &in) {
		return
	}
	h.safo.SetSubrogante(in.Position, in.Unit, in.Actor)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) endSubrogation(w http.ResponseWriter, r *http.Request, _ string) {
	var in subrogation
	if !decode(w, r, &in) {
		return
	}
	h.safo.EndSubrogation(in.Position, in.Unit)
	w.WriteHeader(http.StatusNoContent)
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON, "invalid JSON body")
		return false
	}
	return true
}

func caseID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		response.Error(w, r, http.StatusNotFound, response.CodeNotFound, workflow.ErrCaseNotFound.Error())
		return uuid.Nil, false
	}
	return id, true
}

// fail maps a service error to a response. Unknown errors are logged and
// answered with a generic 500.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var formErr *FormError
	switch {
	case errors.As(err, &formErr):
		response.ValidationError(w, r, formErr.Fields)
	case errors.Is(err, errProcessNotFound), errors.Is(err, workflow.ErrCaseNotFound):
		response.Error(w, r, http.StatusNotFound, response.CodeNotFound, err.Error())
	case errors.Is(err, errNotHolder), errors.Is(err, errNotClaimant), errors.Is(err, errNoAccess):
		response.Error(w, r, http.StatusForbidden, response.CodeForbidden, err.Error())
	case errors.Is(err, workflow.ErrAlreadyAssigned), errors.Is(err, workflow.ErrConflict),
		errors.Is(err, workflow.ErrCaseClosed), errors.Is(err, workflow.ErrInvalidTransition):
		response.Error(w, r, http.StatusConflict, response.CodeConflict, err.Error())
	default:
		h.logger.Error("request failed", "path", r.URL.Path, "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "internal error")
	}
}
