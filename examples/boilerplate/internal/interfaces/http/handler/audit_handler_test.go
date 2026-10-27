package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/httpx/openapi"
	"github.com/kafeiih/vogel/pgxtx"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/apigen"
)

type stubRepo struct {
	entries []audit.Entry
	total   int
	getErr  error
	listErr error
}

func (s *stubRepo) Create(context.Context, pgxtx.DBTX, *audit.Entry) error {
	return nil
}

func (s *stubRepo) GetByID(_ context.Context, id uuid.UUID) (*audit.Entry, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	for i := range s.entries {
		if s.entries[i].ID == id {
			return &s.entries[i], nil
		}
	}
	return nil, pgx.ErrNoRows
}

func (s *stubRepo) List(_ context.Context, f audit.Filters) ([]audit.Entry, int, error) {
	if _, _, err := f.Page(); err != nil {
		return nil, 0, err
	}
	if s.listErr != nil {
		return nil, 0, s.listErr
	}
	return s.entries, s.total, nil
}

func newTestHandler(repo *stubRepo) *AuditHandler {
	return NewAuditHandler(repo, slog.Default())
}

func strictOps(h *AuditHandler) apigen.ServerInterface {
	return apigen.NewStrictHandlerWithOptions(API{AuditHandler: h}, nil, apigen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  openapi.RequestErrorHandler,
		ResponseErrorHandlerFunc: openapi.ResponseErrorHandler(slog.Default()),
	})
}

func list(h *AuditHandler, w http.ResponseWriter, r *http.Request) {
	openapi.RequestInContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		strictOps(h).ListAuditEntries(w, r, apigen.ListAuditEntriesParams{})
	})).ServeHTTP(w, r)
}

func getByID(h *AuditHandler, w http.ResponseWriter, r *http.Request) {
	openapi.RequestInContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		strictOps(h).GetAuditEntry(w, r, chi.URLParam(r, "id"))
	})).ServeHTTP(w, r)
}

func sampleEntry() audit.Entry {
	return audit.Entry{
		ID:                uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		ActorID:           "user-1",
		Username:          "alice",
		OperationCategory: "create",
		OperationName:     "create_item",
		Status:            "success",
		Source:            "http",
		ResourceType:      "item",
		ResourceID:        "42",
		ObjectRepr:        "Item #42",
		IP:                "10.0.0.1",
		UserAgent:         "TestAgent",
		CreatedAt:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestGetByID_ValidUUID_ReturnsEntry(t *testing.T) {
	entry := sampleEntry()
	repo := &stubRepo{entries: []audit.Entry{entry}}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit/"+entry.ID.String(), nil)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", entry.ID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	getByID(h, rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	data := body["data"].(map[string]any)
	if data["resource_type"] != "item" {
		t.Errorf("resource_type = %v, want %q", data["resource_type"], "item")
	}
}

func TestGetByID_InvalidUUID_Returns400(t *testing.T) {
	repo := &stubRepo{}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit/not-a-uuid", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	getByID(h, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGetByID_NotFound_Returns404(t *testing.T) {
	repo := &stubRepo{entries: []audit.Entry{}}
	h := newTestHandler(repo)

	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/audit/"+id.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	getByID(h, rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["code"] != "NOT_FOUND" {
		t.Errorf("code = %v, want %q", body["code"], "NOT_FOUND")
	}
}

func TestGetByID_RepoError_Returns500(t *testing.T) {
	repo := &stubRepo{getErr: context.DeadlineExceeded}
	h := newTestHandler(repo)

	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/audit/"+id.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	getByID(h, rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestGetByID_MissingParam_Returns400(t *testing.T) {
	repo := &stubRepo{}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	getByID(h, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestList_DefaultParams_Returns200(t *testing.T) {
	entry := sampleEntry()
	repo := &stubRepo{entries: []audit.Entry{entry}, total: 1}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit", nil)
	rec := httptest.NewRecorder()

	list(h, rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)

	meta, ok := body["meta"].(map[string]any)
	if !ok {
		t.Fatal("missing 'meta' key")
	}
	if int(meta["total"].(float64)) != 1 {
		t.Errorf("total = %v, want 1", meta["total"])
	}
	if int(meta["limit"].(float64)) != 20 {
		t.Errorf("limit = %v, want 20 (default)", meta["limit"])
	}
	if int(meta["offset"].(float64)) != 0 {
		t.Errorf("offset = %v, want 0 (default)", meta["offset"])
	}
}

func TestList_InvalidLimit_Returns400(t *testing.T) {
	repo := &stubRepo{}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?limit=abc", nil)
	rec := httptest.NewRecorder()

	list(h, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestList_NegativeOffset_Returns400(t *testing.T) {
	repo := &stubRepo{}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?offset=-1", nil)
	rec := httptest.NewRecorder()

	list(h, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestList_OffsetAboveMax_Returns400(t *testing.T) {
	repo := &stubRepo{}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?offset="+strconv.Itoa(audit.MaxListOffset+1), nil)
	rec := httptest.NewRecorder()

	list(h, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["field"] != "offset" {
		t.Errorf("field = %v, want offset (body %s)", body["field"], rec.Body.String())
	}
}

func TestList_OffsetAboveMax_LogsSecurityEvent(t *testing.T) {
	l, records := captureLogger(t)
	h := NewAuditHandler(&stubRepo{}, l)

	req := httptest.NewRequest(http.MethodGet, "/audit?offset="+strconv.Itoa(audit.MaxListOffset+1), nil)
	list(h, httptest.NewRecorder(), req)

	recs := records()
	if len(recs) != 1 {
		t.Fatalf("logged %d records, want 1: %v", len(recs), recs)
	}
	rec := recs[0]
	if rec["level"] != "WARN" || rec["msg"] != "request validation failed" {
		t.Fatalf("record = %v, want the WARN validation event", rec)
	}
	if rec["status"] != float64(http.StatusBadRequest) || rec["code"] != "VALIDATION_ERROR" || rec["path"] != "/audit" {
		t.Fatalf("record = %v, want status 400, VALIDATION_ERROR, path /audit", rec)
	}
	if fields, _ := rec["fields"].([]any); len(fields) != 1 || fields[0] != "offset" {
		t.Fatalf("fields = %v, want [offset]", rec["fields"])
	}
}

func TestList_OffsetAtMax_Returns200(t *testing.T) {
	repo := &stubRepo{}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?offset="+strconv.Itoa(audit.MaxListOffset), nil)
	rec := httptest.NewRecorder()

	list(h, rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestList_InvalidOperationCategory_Returns400(t *testing.T) {
	repo := &stubRepo{}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?operation_category=INVALID", nil)
	rec := httptest.NewRecorder()

	list(h, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestList_InvalidDate_Returns400(t *testing.T) {
	repo := &stubRepo{}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?from=not-a-date", nil)
	rec := httptest.NewRecorder()

	list(h, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestList_ValidOperationCategory_Returns200(t *testing.T) {
	repo := &stubRepo{entries: []audit.Entry{}, total: 0}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?operation_category=create", nil)
	rec := httptest.NewRecorder()

	list(h, rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestList_LimitClamped_To100(t *testing.T) {
	repo := &stubRepo{entries: []audit.Entry{}, total: 0}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?limit=500", nil)
	rec := httptest.NewRecorder()

	list(h, rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	meta := body["meta"].(map[string]any)
	if int(meta["limit"].(float64)) != 100 {
		t.Errorf("limit = %v, want 100 (clamped)", meta["limit"])
	}
}

func TestList_InvalidStatus_Returns400(t *testing.T) {
	repo := &stubRepo{}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?status=INVALID", nil)
	rec := httptest.NewRecorder()

	list(h, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestList_ValidStatus_Returns200(t *testing.T) {
	repo := &stubRepo{entries: []audit.Entry{}, total: 0}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?status=success", nil)
	rec := httptest.NewRecorder()

	list(h, rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestList_InternalError_Returns500(t *testing.T) {
	repo := &stubRepo{listErr: context.DeadlineExceeded}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit", nil)
	rec := httptest.NewRecorder()

	list(h, rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
