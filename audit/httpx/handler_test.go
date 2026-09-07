package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/pgxtx"
)

// fakeRepository is an in-memory audit.Repository used to test Handler
// without a database. Mirrors workflow/engine_test.go's fakeRepository
// style: fields configure the canned return value or error per test.
type fakeRepository struct {
	getEntry *audit.Entry
	getErr   error

	listEntries []audit.Entry
	listTotal   int
	listErr     error

	gotFilters audit.Filters
	gotID      uuid.UUID
}

func (f *fakeRepository) Create(_ context.Context, _ pgxtx.DBTX, _ *audit.Entry) error {
	return nil
}

func (f *fakeRepository) GetByID(_ context.Context, id uuid.UUID) (*audit.Entry, error) {
	f.gotID = id
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getEntry, nil
}

func (f *fakeRepository) List(_ context.Context, filters audit.Filters) ([]audit.Entry, int, error) {
	f.gotFilters = filters
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	return f.listEntries, f.listTotal, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func decodeSuccess(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	return body
}

func TestNewHandler_NilRepoReturnsError(t *testing.T) {
	_, err := NewHandler(nil, discardLogger())
	if err == nil {
		t.Fatal("expected an error for a nil repository")
	}
}

func TestNewHandler_NilLoggerReturnsError(t *testing.T) {
	_, err := NewHandler(&fakeRepository{}, nil)
	if err == nil {
		t.Fatal("expected an error for a nil logger")
	}
}

func TestHandler_List_Success(t *testing.T) {
	entries := []audit.Entry{
		{ID: uuid.New(), ResourceType: "invoice"},
		{ID: uuid.New(), ResourceType: "order"},
	}
	repo := &fakeRepository{listEntries: entries, listTotal: 2}
	h, err := NewHandler(repo, discardLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/audit", nil)
	rec := httptest.NewRecorder()
	h.List(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := decodeSuccess(t, rec)
	data, ok := body["data"].([]any)
	if !ok || len(data) != 2 {
		t.Fatalf("data = %v, want a list of 2 items", body["data"])
	}
	meta, ok := body["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta missing or wrong type: %v", body["meta"])
	}
	if meta["total"] != float64(2) {
		t.Errorf("meta.total = %v, want 2", meta["total"])
	}
}

func TestHandler_List_ValidationFailureReturns400(t *testing.T) {
	repo := &fakeRepository{}
	h, err := NewHandler(repo, discardLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/audit?resource_id=not-a-uuid", nil)
	rec := httptest.NewRecorder()
	h.List(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandler_List_RepositoryErrorReturns500(t *testing.T) {
	repo := &fakeRepository{listErr: errors.New("boom")}
	h, err := NewHandler(repo, discardLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/audit", nil)
	rec := httptest.NewRecorder()
	h.List(rec, r)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	body := decodeSuccess(t, rec)
	if body["message"] != DefaultMessages().ListFailed {
		t.Errorf("message = %v, want default %q", body["message"], DefaultMessages().ListFailed)
	}
}

func TestHandler_List_CustomMessageOnRepositoryError(t *testing.T) {
	repo := &fakeRepository{listErr: errors.New("boom")}
	h, err := NewHandler(repo, discardLogger(), WithMessages(Messages{ListFailed: "custom list failure"}))
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/audit", nil)
	rec := httptest.NewRecorder()
	h.List(rec, r)

	body := decodeSuccess(t, rec)
	if body["message"] != "custom list failure" {
		t.Errorf("message = %v, want %q", body["message"], "custom list failure")
	}
}

func mountGetByID(h *Handler) *chi.Mux {
	router := chi.NewRouter()
	router.Get("/audit/{id}", h.GetByID)
	return router
}

func TestHandler_GetByID_Success(t *testing.T) {
	id := uuid.New()
	entry := &audit.Entry{ID: id, ResourceType: "invoice", ResourceID: "inv-1"}
	repo := &fakeRepository{getEntry: entry}
	h, err := NewHandler(repo, discardLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	router := mountGetByID(h)

	r := httptest.NewRequest(http.MethodGet, "/audit/"+id.String(), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := decodeSuccess(t, rec)
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data missing or wrong type: %v", body["data"])
	}
	if data["id"] != id.String() {
		t.Errorf("data.id = %v, want %q", data["id"], id.String())
	}
	if data["resource_id"] != "inv-1" {
		t.Errorf("data.resource_id = %v, want inv-1", data["resource_id"])
	}
}

func TestHandler_GetByID_NotFoundReturns404(t *testing.T) {
	repo := &fakeRepository{getErr: pgx.ErrNoRows}
	h, err := NewHandler(repo, discardLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	router := mountGetByID(h)

	r := httptest.NewRequest(http.MethodGet, "/audit/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	body := decodeSuccess(t, rec)
	if body["message"] != DefaultMessages().NotFound {
		t.Errorf("message = %v, want default %q", body["message"], DefaultMessages().NotFound)
	}
}

func TestHandler_GetByID_CustomMessageOnNotFound(t *testing.T) {
	repo := &fakeRepository{getErr: pgx.ErrNoRows}
	h, err := NewHandler(repo, discardLogger(), WithMessages(Messages{NotFound: "custom not found"}))
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	router := mountGetByID(h)

	r := httptest.NewRequest(http.MethodGet, "/audit/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)

	body := decodeSuccess(t, rec)
	if body["message"] != "custom not found" {
		t.Errorf("message = %v, want %q", body["message"], "custom not found")
	}
}

func TestHandler_GetByID_RepositoryErrorReturns500(t *testing.T) {
	repo := &fakeRepository{getErr: errors.New("boom")}
	h, err := NewHandler(repo, discardLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	router := mountGetByID(h)

	r := httptest.NewRequest(http.MethodGet, "/audit/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	body := decodeSuccess(t, rec)
	if body["message"] != DefaultMessages().GetFailed {
		t.Errorf("message = %v, want default %q", body["message"], DefaultMessages().GetFailed)
	}
}

func TestHandler_GetByID_InvalidUUIDReturns400(t *testing.T) {
	repo := &fakeRepository{}
	h, err := NewHandler(repo, discardLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	router := mountGetByID(h)

	r := httptest.NewRequest(http.MethodGet, "/audit/not-a-uuid", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
