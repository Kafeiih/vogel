package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/workflow"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/application/process"
	domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
)

const maxProcessBodyBytes = 1 << 20

type ProcessService interface {
	Publish(ctx context.Context, spec domain.ProcessSpec) (*domain.Process, error)
	Latest(name string) (*domain.Process, bool)
	Start(ctx context.Context, actor, process, action string, values map[string]any) (*workflow.Case, error)
	Inbox(ctx context.Context, actor string) ([]workflow.Case, error)
	View(ctx context.Context, actor string, caseID uuid.UUID) (*process.CaseView, error)
	Claim(ctx context.Context, actor string, caseID uuid.UUID) (*workflow.Case, error)
	Submit(ctx context.Context, actor string, caseID uuid.UUID, action string, values map[string]any) (*workflow.Case, error)
}

type ProcessHandler struct {
	svc    ProcessService
	logger *slog.Logger
}

func NewProcessHandler(svc ProcessService, logger *slog.Logger) *ProcessHandler {
	return &ProcessHandler{svc: svc, logger: logger}
}

type submission struct {
	Action string         `json:"action"`
	Values map[string]any `json:"values"`
}

type caseResponse struct {
	ID         uuid.UUID  `json:"id"`
	Process    string     `json:"process"`
	Version    int        `json:"version"`
	State      string     `json:"state"`
	Status     string     `json:"status"`
	AssignedTo string     `json:"assignedTo,omitempty"`
	OpenedAt   time.Time  `json:"openedAt"`
	ClosedAt   *time.Time `json:"closedAt,omitempty"`
}

func toCaseResponse(c *workflow.Case) caseResponse {
	return caseResponse{
		ID: c.ID, Process: c.Definition, Version: c.Version, State: c.State,
		Status: string(c.Status), AssignedTo: c.AssignedTo, OpenedAt: c.OpenedAt, ClosedAt: c.ClosedAt,
	}
}

type caseViewResponse struct {
	caseResponse
	Values  map[string]any     `json:"values"`
	History []domain.CaseValue `json:"history"`
}

func (h *ProcessHandler) Publish(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProcessBodyBytes))
	if err != nil {
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON, "No se pudo leer el cuerpo")
		return
	}
	spec, err := domain.ParseProcessSpec(raw)
	if err != nil {
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON, err.Error())
		return
	}
	p, err := h.svc.Publish(r.Context(), spec)
	if err != nil {
		var compileErr *domain.CompileError
		if errors.As(err, &compileErr) {
			response.Error(w, r, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity, err.Error())
			return
		}
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusCreated, map[string]any{"name": p.Definition.Name, "version": p.Definition.Version})
}

func (h *ProcessHandler) Latest(w http.ResponseWriter, r *http.Request) {
	p, ok := h.svc.Latest(chi.URLParam(r, "name"))
	if !ok {
		response.Error(w, r, http.StatusNotFound, response.CodeNotFound, domain.ErrProcessNotFound.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"version": p.Definition.Version, "spec": p.Spec})
}

func (h *ProcessHandler) Start(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorOf(w, r)
	if !ok {
		return
	}
	var in submission
	if !decodeStrict(w, r, &in) {
		return
	}
	c, err := h.svc.Start(r.Context(), actor, chi.URLParam(r, "name"), in.Action, in.Values)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusCreated, toCaseResponse(c))
}

func (h *ProcessHandler) Inbox(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorOf(w, r)
	if !ok {
		return
	}
	cases, err := h.svc.Inbox(r.Context(), actor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]caseResponse, 0, len(cases))
	for i := range cases {
		out = append(out, toCaseResponse(&cases[i]))
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *ProcessHandler) View(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := actorAndCase(w, r)
	if !ok {
		return
	}
	v, err := h.svc.View(r.Context(), actor, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, caseViewResponse{caseResponse: toCaseResponse(v.Case), Values: v.Values, History: v.History})
}

func (h *ProcessHandler) Claim(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := actorAndCase(w, r)
	if !ok {
		return
	}
	c, err := h.svc.Claim(r.Context(), actor, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toCaseResponse(c))
}

func (h *ProcessHandler) Submit(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := actorAndCase(w, r)
	if !ok {
		return
	}
	var in submission
	if !decodeStrict(w, r, &in) {
		return
	}
	c, err := h.svc.Submit(r.Context(), actor, id, in.Action, in.Values)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, toCaseResponse(c))
}

func actorOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	p := auth.FromContext(r.Context())
	if p == nil || p.UserID == "" {
		response.Error(w, r, http.StatusUnauthorized, response.CodeUnauthorized, "Autenticación requerida")
		return "", false
	}
	return p.UserID, true
}

func actorAndCase(w http.ResponseWriter, r *http.Request) (string, uuid.UUID, bool) {
	actor, ok := actorOf(w, r)
	if !ok {
		return "", uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, http.StatusNotFound, response.CodeNotFound, workflow.ErrCaseNotFound.Error())
		return "", uuid.Nil, false
	}
	return actor, id, true
}

func decodeStrict(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProcessBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON, "JSON inválido")
		return false
	}
	return true
}

func (h *ProcessHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var formErr *domain.FormError
	switch {
	case errors.As(err, &formErr):
		response.ValidationError(w, r, formErr.Fields)
	case errors.Is(err, domain.ErrProcessNotFound), errors.Is(err, workflow.ErrCaseNotFound):
		response.Error(w, r, http.StatusNotFound, response.CodeNotFound, err.Error())
	case errors.Is(err, domain.ErrNotHolder), errors.Is(err, domain.ErrNotClaimant), errors.Is(err, domain.ErrNoAccess):
		response.Error(w, r, http.StatusForbidden, response.CodeForbidden, err.Error())
	case errors.Is(err, workflow.ErrAlreadyAssigned), errors.Is(err, workflow.ErrConflict),
		errors.Is(err, workflow.ErrCaseClosed), errors.Is(err, workflow.ErrInvalidTransition),
		errors.Is(err, domain.ErrPublishConflict):
		response.Error(w, r, http.StatusConflict, response.CodeConflict, err.Error())
	default:
		h.logger.ErrorContext(r.Context(), "bpms request failed", "path", r.URL.Path, "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "Error interno")
	}
}
