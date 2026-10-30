package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/httpx/openapi"
	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/request"
	"github.com/kafeiih/vogel/workflow"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/application/process"
	domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/bpmsgen"
)

const maxProcessBodyBytes = 1 << 20

type ProcessService interface {
	Publish(ctx context.Context, spec domain.ProcessSpec) (*domain.Process, error)
	Latest(name string) (*domain.Process, bool)
	Start(ctx context.Context, actor, process, action string, values map[string]any) (*workflow.Case, error)
	Inbox(ctx context.Context, actor string) ([]workflow.WorkItem, error)
	View(ctx context.Context, actor string, caseID uuid.UUID) (*process.CaseView, error)
	Claim(ctx context.Context, actor string, caseID, tokenID uuid.UUID) (*workflow.Case, error)
	Submit(ctx context.Context, actor string, caseID, tokenID uuid.UUID, action string, values map[string]any) (*workflow.Case, error)
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

// submitRequest is submission plus the work item to move.
type submitRequest struct {
	submission
	TokenID *uuid.UUID `json:"tokenId"`
}

// Mount registers the bpms operations of api/openapi.yaml on r, each behind
// guard(kind, action). The body checks run before the generated decoder, in
// the order the responses depend on: caller, case, then body.
func (h *ProcessHandler) Mount(r chi.Router, guard func(kind, action string) func(http.Handler) http.Handler) {
	ops := bpmsgen.NewStrictHandlerWithOptions(h, nil, bpmsgen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  openapi.RequestErrorHandler,
		ResponseErrorHandlerFunc: openapi.ResponseErrorHandler(h.logger),
	})
	byName := func(op func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { op(w, r, chi.URLParam(r, "name")) }
	}
	byID := func(op func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { op(w, r, chi.URLParam(r, "id")) }
	}

	r.With(guard("bpms:process", "publish"), openapi.Decode(h.checkSpec), openapi.RequestInContext).
		Post("/processes", ops.PublishProcess)
	r.With(guard("bpms:process", "read"), openapi.RequestInContext).
		Get("/processes/{name}", byName(ops.GetLatestProcess))
	r.With(guard("bpms:process", "start"), openapi.Decode(h.checkStart), openapi.RequestInContext).
		Post("/processes/{name}/cases", byName(ops.StartCase))
	r.With(guard("bpms:case", "inbox"), openapi.RequestInContext).
		Get("/inbox", ops.ListInbox)
	r.With(guard("bpms:case", "view"), openapi.RequestInContext).
		Get("/cases/{id}", byID(ops.GetCase))
	r.With(guard("bpms:case", "claim"), openapi.RequestInContext).
		Post("/cases/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
			var params bpmsgen.ClaimCaseParams
			if raw := r.URL.Query().Get("tokenId"); raw != "" {
				tokenID, err := uuid.Parse(raw)
				if err != nil {
					response.Error(w, r, http.StatusNotFound, response.CodeNotFound, workflow.ErrTokenNotFound.Error())
					return
				}
				params.TokenId = &tokenID
			}
			ops.ClaimCase(w, r, chi.URLParam(r, "id"), params)
		})
	r.With(guard("bpms:case", "submit"), openapi.Decode(h.checkSubmit), openapi.RequestInContext).
		Post("/cases/{id}/submit", byID(ops.SubmitCase))
}

func (h *ProcessHandler) checkSpec(w http.ResponseWriter, r *http.Request) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProcessBodyBytes))
	if err != nil {
		request.LogRejection(h.logger, r, http.StatusBadRequest, response.CodeInvalidJSON)
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON, "No se pudo leer el cuerpo")
		return false
	}
	if _, err := domain.ParseProcessSpec(raw); err != nil {
		request.LogRejection(h.logger, r, http.StatusBadRequest, response.CodeInvalidJSON)
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON, err.Error())
		return false
	}
	return true
}

func (h *ProcessHandler) checkStart(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := actorOf(w, r); !ok {
		return false
	}
	var in submission
	return h.decodeStrict(w, r, &in)
}

func (h *ProcessHandler) checkSubmit(w http.ResponseWriter, r *http.Request) bool {
	if _, _, ok := actorAndCase(w, r); !ok {
		return false
	}
	var in submitRequest
	return h.decodeStrict(w, r, &in)
}

func (h *ProcessHandler) PublishProcess(ctx context.Context, req bpmsgen.PublishProcessRequestObject) (bpmsgen.PublishProcessResponseObject, error) {
	spec, err := domain.ParseProcessSpec(*req.Body)
	if err != nil {
		return nil, openapi.Respond(http.StatusBadRequest, response.CodeInvalidJSON, err.Error())
	}
	p, err := h.svc.Publish(ctx, spec)
	if err != nil {
		var compileErr *domain.CompileError
		if errors.As(err, &compileErr) {
			return nil, openapi.Respond(http.StatusUnprocessableEntity, response.CodeUnprocessableEntity, err.Error())
		}
		return nil, h.failure(err)
	}
	return bpmsgen.PublishProcess201JSONResponse{
		Success: true, Message: "OK",
		Data: bpmsgen.PublishedProcess{Name: p.Definition.Name, Version: p.Definition.Version},
	}, nil
}

func (h *ProcessHandler) GetLatestProcess(_ context.Context, req bpmsgen.GetLatestProcessRequestObject) (bpmsgen.GetLatestProcessResponseObject, error) {
	p, ok := h.svc.Latest(req.Name)
	if !ok {
		return nil, openapi.Respond(http.StatusNotFound, response.CodeNotFound, domain.ErrProcessNotFound.Error())
	}
	spec, err := json.Marshal(p.Spec)
	if err != nil {
		return nil, h.failure(err)
	}
	return bpmsgen.GetLatestProcess200JSONResponse{
		Success: true, Message: "OK",
		Data: bpmsgen.ProcessVersion{Spec: spec, Version: p.Definition.Version},
	}, nil
}

func (h *ProcessHandler) StartCase(ctx context.Context, req bpmsgen.StartCaseRequestObject) (bpmsgen.StartCaseResponseObject, error) {
	actor, err := actorFrom(ctx)
	if err != nil {
		return nil, err
	}
	action, values := fromSubmission(req.Body)
	c, err := h.svc.Start(ctx, actor, req.Name, action, values)
	if err != nil {
		return nil, h.failure(err)
	}
	return bpmsgen.StartCase201JSONResponse{Success: true, Message: "OK", Data: toCase(c)}, nil
}

func (h *ProcessHandler) ListInbox(ctx context.Context, _ bpmsgen.ListInboxRequestObject) (bpmsgen.ListInboxResponseObject, error) {
	actor, err := actorFrom(ctx)
	if err != nil {
		return nil, err
	}
	items, err := h.svc.Inbox(ctx, actor)
	if err != nil {
		return nil, h.failure(err)
	}
	out := make([]bpmsgen.Case, 0, len(items))
	for i := range items {
		out = append(out, toWorkItem(&items[i]))
	}
	return bpmsgen.ListInbox200JSONResponse{Success: true, Message: "OK", Data: out}, nil
}

func (h *ProcessHandler) GetCase(ctx context.Context, req bpmsgen.GetCaseRequestObject) (bpmsgen.GetCaseResponseObject, error) {
	actor, id, err := actorAndCaseFrom(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.View(ctx, actor, id)
	if err != nil {
		return nil, h.failure(err)
	}
	return bpmsgen.GetCase200JSONResponse{Success: true, Message: "OK", Data: toCaseView(v)}, nil
}

func (h *ProcessHandler) ClaimCase(ctx context.Context, req bpmsgen.ClaimCaseRequestObject) (bpmsgen.ClaimCaseResponseObject, error) {
	actor, id, err := actorAndCaseFrom(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	var tokenID uuid.UUID
	if req.Params.TokenId != nil {
		tokenID = *req.Params.TokenId
	}
	c, err := h.svc.Claim(ctx, actor, id, tokenID)
	if err != nil {
		return nil, h.failure(err)
	}
	return bpmsgen.ClaimCase200JSONResponse{Success: true, Message: "OK", Data: toCase(c)}, nil
}

func (h *ProcessHandler) SubmitCase(ctx context.Context, req bpmsgen.SubmitCaseRequestObject) (bpmsgen.SubmitCaseResponseObject, error) {
	actor, id, err := actorAndCaseFrom(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	var tokenID uuid.UUID
	if req.Body.TokenId != nil {
		tokenID = *req.Body.TokenId
	}
	action, values := fromSubmission(&bpmsgen.Submission{Action: req.Body.Action, Values: req.Body.Values})
	c, err := h.svc.Submit(ctx, actor, id, tokenID, action, values)
	if err != nil {
		return nil, h.failure(err)
	}
	return bpmsgen.SubmitCase200JSONResponse{Success: true, Message: "OK", Data: toCase(c)}, nil
}

func fromSubmission(in *bpmsgen.Submission) (string, map[string]any) {
	var action string
	var values map[string]any
	if in.Action != nil {
		action = *in.Action
	}
	if in.Values != nil {
		values = *in.Values
	}
	return action, values
}

func toCase(c *workflow.Case) bpmsgen.Case {
	out := bpmsgen.Case{
		Id: c.ID, Process: c.Definition, Version: c.Version, State: c.State,
		Status: string(c.Status), OpenedAt: c.OpenedAt, ClosedAt: c.ClosedAt,
	}
	if c.AssignedTo != "" {
		out.AssignedTo = &c.AssignedTo
	}
	return out
}

// toWorkItem is the inbox entry of one token: its case, with the token's
// node, assignee and ID.
func toWorkItem(it *workflow.WorkItem) bpmsgen.Case {
	out := toCase(&it.Case)
	out.State = it.Token.Node
	out.AssignedTo = nil
	if it.Token.AssignedTo != "" {
		out.AssignedTo = &it.Token.AssignedTo
	}
	out.TokenId = &it.Token.ID
	return out
}

func toCaseView(v *process.CaseView) bpmsgen.CaseView {
	c := toCase(v.Case)
	out := bpmsgen.CaseView{
		Id: c.Id, Process: c.Process, Version: c.Version, State: c.State, Status: c.Status,
		AssignedTo: c.AssignedTo, OpenedAt: c.OpenedAt, ClosedAt: c.ClosedAt, Values: v.Values,
	}
	if v.History != nil {
		out.History = make([]bpmsgen.CaseValue, len(v.History))
		for i, hv := range v.History {
			out.History[i] = bpmsgen.CaseValue{Field: hv.Field, Value: hv.Value, NodeId: hv.NodeID, ActorId: hv.ActorID, RecordedAt: hv.RecordedAt}
		}
	}
	return out
}

func actorFrom(ctx context.Context) (string, error) {
	p := auth.FromContext(ctx)
	if p == nil || p.UserID == "" {
		return "", openapi.Respond(http.StatusUnauthorized, response.CodeUnauthorized, "Autenticación requerida")
	}
	return p.UserID, nil
}

func actorAndCaseFrom(ctx context.Context, rawID string) (string, uuid.UUID, error) {
	actor, err := actorFrom(ctx)
	if err != nil {
		return "", uuid.Nil, err
	}
	id, err := uuid.Parse(rawID)
	if err != nil {
		return "", uuid.Nil, openapi.Respond(http.StatusNotFound, response.CodeNotFound, workflow.ErrCaseNotFound.Error())
	}
	return actor, id, nil
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

func (h *ProcessHandler) decodeStrict(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProcessBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		request.LogRejection(h.logger, r, http.StatusBadRequest, response.CodeInvalidJSON)
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON, "JSON inválido")
		return false
	}
	return true
}

func (h *ProcessHandler) failure(err error) error {
	return openapi.Fail(err, func(w http.ResponseWriter, r *http.Request) { h.fail(w, r, err) })
}

func (h *ProcessHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var formErr *domain.FormError
	switch {
	case errors.As(err, &formErr):
		request.LogValidationFailure(h.logger, r, formErr.Fields)
		response.ValidationError(w, r, formErr.Fields)
	case errors.Is(err, domain.ErrProcessNotFound), errors.Is(err, workflow.ErrCaseNotFound),
		errors.Is(err, workflow.ErrTokenNotFound):
		response.Error(w, r, http.StatusNotFound, response.CodeNotFound, err.Error())
	case errors.Is(err, domain.ErrNotHolder), errors.Is(err, domain.ErrNotClaimant), errors.Is(err, domain.ErrNoAccess):
		response.Error(w, r, http.StatusForbidden, response.CodeForbidden, err.Error())
	case errors.Is(err, workflow.ErrAlreadyAssigned), errors.Is(err, workflow.ErrConflict),
		errors.Is(err, workflow.ErrTokenRequired),
		errors.Is(err, workflow.ErrCaseClosed), errors.Is(err, workflow.ErrInvalidTransition),
		errors.Is(err, domain.ErrPublishConflict):
		response.Error(w, r, http.StatusConflict, response.CodeConflict, err.Error())
	default:
		h.logger.ErrorContext(r.Context(), "bpms request failed", "path", r.URL.Path, "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "Error interno")
	}
}
