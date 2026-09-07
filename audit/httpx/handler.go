package httpx

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/request"
)

// Handler serves the audit log over HTTP: List and GetByID. It delegates
// straight to an audit.Repository — there is no separate application-query
// layer, since vogel already exposes Repository directly.
type Handler struct {
	repo     audit.Repository
	logger   *slog.Logger
	messages Messages
}

// NewHandler creates a Handler backed by repo, logging failures with
// logger. Pass WithMessages to localize the user-facing error strings;
// without it, Handler uses DefaultMessages.
func NewHandler(repo audit.Repository, logger *slog.Logger, opts ...Option) (*Handler, error) {
	if repo == nil {
		return nil, fmt.Errorf("audit/httpx: audit.Repository is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("audit/httpx: logger is required")
	}

	messages := DefaultMessages()
	for _, opt := range opts {
		opt(&messages)
	}

	return &Handler{repo: repo, logger: logger, messages: messages}, nil
}

// List handles a request to list audit log entries, filtered and paginated
// via FiltersFromRequest. It responds with a response.JSONList envelope on
// success, a 400 with per-field errors on invalid filters, and a 500 on any
// repository failure.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	filters := FiltersFromRequest(v, r)

	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	entries, total, err := h.repo.List(r.Context(), filters)
	if err != nil {
		h.logger.Error("listing audit entries", "error", err, "path", r.URL.Path, "query", r.URL.RawQuery)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, h.messages.ListFailed)
		return
	}

	response.JSONList(w, http.StatusOK, ToResponseList(entries), total, filters.Limit, filters.Offset)
}

// GetByID handles a request to fetch a single audit log entry by its "id"
// route parameter. It responds with a response.JSON envelope on success, a
// 400 if "id" is not a valid UUID, a 404 if no entry matches, and a 500 on
// any other repository failure.
func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	id := v.UUIDParam(r, "id")

	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	entry, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, http.StatusNotFound, response.CodeNotFound, h.messages.NotFound)
			return
		}
		h.logger.Error("getting audit entry", "error", err, "id", id)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, h.messages.GetFailed)
		return
	}

	response.JSON(w, http.StatusOK, ToResponse(entry))
}
