package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/audit/httpx"
	"github.com/kafeiih/vogel/httpx/openapi"
	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/request"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/apigen"
)

type AuditHandler struct {
	repo   audit.Repository
	logger *slog.Logger
}

func NewAuditHandler(repo audit.Repository, logger *slog.Logger) *AuditHandler {
	return &AuditHandler{
		repo:   repo,
		logger: logger,
	}
}

func (h *AuditHandler) ListAuditEntries(ctx context.Context, _ apigen.ListAuditEntriesRequestObject) (apigen.ListAuditEntriesResponseObject, error) {
	r := openapi.Request(ctx)
	v := request.NewValidator()
	filters := httpx.FiltersFromRequest(v, r)

	if v.HasErrors() {
		return nil, openapi.Invalid(v)
	}

	entries, total, err := h.repo.List(ctx, filters)
	if errors.Is(err, audit.ErrOffsetOutOfRange) {
		request.LogValidationFailure(h.logger, r, request.FieldErrors{"offset": ""})
		return nil, openapi.Respond(http.StatusBadRequest, response.CodeValidationError,
			"El offset está fuera de rango; acotá los filtros en lugar de seguir paginando",
			response.WithField("offset"))
	}
	if err != nil {
		h.logger.Error("listing audit entries", "error", err, "path", r.URL.Path, "query", r.URL.RawQuery)
		return nil, openapi.Respond(http.StatusInternalServerError, response.CodeInternalError, "No se pudo listar las entradas de auditoría")
	}

	return apigen.ListAuditEntries200JSONResponse{
		Success: true,
		Message: "OK",
		Data:    httpx.ToResponseList(entries),
		Meta:    apigen.ListMeta{Total: total, Limit: filters.Limit, Offset: filters.Offset},
	}, nil
}

func (h *AuditHandler) GetAuditEntry(ctx context.Context, _ apigen.GetAuditEntryRequestObject) (apigen.GetAuditEntryResponseObject, error) {
	r := openapi.Request(ctx)
	v := request.NewValidator()
	id := v.UUIDParam(r, "id")

	if v.HasErrors() {
		return nil, openapi.Invalid(v)
	}

	entry, err := h.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, openapi.Respond(http.StatusNotFound, response.CodeNotFound, "Entrada de auditoría no encontrada")
		}
		h.logger.Error("getting audit entry", "error", err, "id", id)
		return nil, openapi.Respond(http.StatusInternalServerError, response.CodeInternalError, "No se pudo obtener la entrada de auditoría")
	}

	return apigen.GetAuditEntry200JSONResponse{Success: true, Message: "OK", Data: httpx.ToResponse(entry)}, nil
}
