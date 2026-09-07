package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/pgxtx"
)

// --- fake repository for handler tests ---

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

func (s *stubRepo) List(_ context.Context, _ audit.Filters) ([]audit.Entry, int, error) {
	if s.listErr != nil {
		return nil, 0, s.listErr
	}
	return s.entries, s.total, nil
}

func newTestHandler(repo *stubRepo) *AuditHandler {
	return NewAuditHandler(repo, slog.Default())
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

// --- GetByID tests ---

func TestGetByID_ValidUUID_ReturnsEntry(t *testing.T) {
	entry := sampleEntry()
	repo := &stubRepo{entries: []audit.Entry{entry}}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit/"+entry.ID.String(), nil)

	// Set chi URL param
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", entry.ID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.GetByID(rec, req)

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
	h.GetByID(rec, req)

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
	h.GetByID(rec, req)

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
	h.GetByID(rec, req)

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
	h.GetByID(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// --- List tests ---

func TestList_DefaultParams_Returns200(t *testing.T) {
	entry := sampleEntry()
	repo := &stubRepo{entries: []audit.Entry{entry}, total: 1}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit", nil)
	rec := httptest.NewRecorder()

	h.List(rec, req)

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

	h.List(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestList_NegativeOffset_Returns400(t *testing.T) {
	repo := &stubRepo{}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?offset=-1", nil)
	rec := httptest.NewRecorder()

	h.List(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestList_InvalidOperationCategory_Returns400(t *testing.T) {
	repo := &stubRepo{}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?operation_category=INVALID", nil)
	rec := httptest.NewRecorder()

	h.List(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestList_InvalidDate_Returns400(t *testing.T) {
	repo := &stubRepo{}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?from=not-a-date", nil)
	rec := httptest.NewRecorder()

	h.List(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestList_ValidOperationCategory_Returns200(t *testing.T) {
	repo := &stubRepo{entries: []audit.Entry{}, total: 0}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?operation_category=create", nil)
	rec := httptest.NewRecorder()

	h.List(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestList_LimitClamped_To100(t *testing.T) {
	repo := &stubRepo{entries: []audit.Entry{}, total: 0}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?limit=500", nil)
	rec := httptest.NewRecorder()

	h.List(rec, req)

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

	h.List(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestList_ValidStatus_Returns200(t *testing.T) {
	repo := &stubRepo{entries: []audit.Entry{}, total: 0}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit?status=success", nil)
	rec := httptest.NewRecorder()

	h.List(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestList_InternalError_Returns500(t *testing.T) {
	repo := &stubRepo{listErr: context.DeadlineExceeded}
	h := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/audit", nil)
	rec := httptest.NewRecorder()

	h.List(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
