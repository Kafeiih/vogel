package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/audit/httpx"
	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/request"
)

// AuditHandler is a thin wrapper around vogel/audit/httpx's parsing and
// mapping helpers. It exists as an app-owned type — instead of mounting
// httpx.Handler directly — solely so the Swagger annotations below (read by
// swaggo from the comments on the concrete function, never from an imported
// library type) stay part of this application's generated docs.
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

// List godoc
//
//	@Summary		List audit entries
//	@Description	Returns a filtered, paginated list of audit log entries
//	@Tags			audit
//	@Produce		json
//	@Param			actor_id			query		string	false	"Filter by actor ID"
//	@Param			resource_type		query		string	false	"Filter by resource type"
//	@Param			resource_id			query		string	false	"Filtrar por ID de recurso (formato UUID)"	Format(uuid)
//	@Param			operation_category	query		string	false	"Filter by operation category"	Enums(create, update, delete, execute)
//	@Param			operation_name		query		string	false	"Filter by operation name"
//	@Param			status				query		string	false	"Filter by status"				Enums(success, failed, partial, noop)
//	@Param			aggregate_type		query		string	false	"Filter by aggregate type"
//	@Param			aggregate_id		query		string	false	"Filter by aggregate ID"
//	@Param			from				query		string	false	"Start date (RFC3339)"
//	@Param			to					query		string	false	"End date (RFC3339)"
//	@Param			limit				query		int		false	"Page size (max 100)"			default(20)
//	@Param			offset				query		int		false	"Offset"						default(0)
//	@Success		200					{object}	response.ListResponse
//	@Failure		400					{object}	response.ValidationErrorResponse
//	@Failure		500					{object}	response.ErrorResponse
//	@Security		ApiKeyAuth
//	@Router			/audit [get]
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	filters := httpx.FiltersFromRequest(v, r)

	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	entries, total, err := h.repo.List(r.Context(), filters)
	if err != nil {
		h.logger.Error("listing audit entries", "error", err, "path", r.URL.Path, "query", r.URL.RawQuery)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "No se pudo listar las entradas de auditoría")
		return
	}

	response.JSONList(w, http.StatusOK, httpx.ToResponseList(entries), total, filters.Limit, filters.Offset)
}

// GetByID godoc
//
//	@Summary		Get audit entry
//	@Description	Returns a single audit log entry by ID
//	@Tags			audit
//	@Produce		json
//	@Param			id	path		string	true	"Audit entry UUID"
//	@Success		200	{object}	response.SuccessResponse
//	@Failure		400	{object}	response.ValidationErrorResponse
//	@Failure		404	{object}	response.ErrorResponse
//	@Failure		500	{object}	response.ErrorResponse
//	@Security		ApiKeyAuth
//	@Router			/audit/{id} [get]
func (h *AuditHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	id := v.UUIDParam(r, "id")

	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	entry, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, http.StatusNotFound, response.CodeNotFound, "Entrada de auditoría no encontrada")
			return
		}
		h.logger.Error("getting audit entry", "error", err, "id", id)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "No se pudo obtener la entrada de auditoría")
		return
	}

	response.JSON(w, http.StatusOK, httpx.ToResponse(entry))
}
