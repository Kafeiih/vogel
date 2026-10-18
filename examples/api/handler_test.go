package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kafeiih/vogel/auth"
)

// These tests cover only the rejections DocumentHandler.Create makes before
// it opens its transaction: the success path runs tx.WithTx against a real
// pgxpool.Pool, so proving that a created document's OwnerID equals the
// principal's UserID needs a database: see
// TestDocumentHandlerCreate_PersistsThePrincipalAsOwner in
// handler_integration_test.go (build tag "integration"). The handler here is
// built with no store, engine or pool on purpose: if any of these cases
// reached the transaction, the nil pool would panic and fail the test.
func TestDocumentHandlerCreate_RejectsBeforeTheTransaction(t *testing.T) {
	const validBody = `{"title":"Contrato","kind":"contract","issued_at":"2026-01-02"}`

	tests := []struct {
		name         string
		principal    *auth.Principal
		body         string
		wantStatus   int
		wantInBody   string
		wantNotFound string
	}{
		{
			name:       "no authenticated principal is 401",
			principal:  nil,
			body:       validBody,
			wantStatus: http.StatusUnauthorized,
			wantInBody: "UNAUTHORIZED",
		},
		{
			name:       "principal with an empty user ID is 401",
			principal:  &auth.Principal{},
			body:       validBody,
			wantStatus: http.StatusUnauthorized,
			wantInBody: "UNAUTHORIZED",
		},
		{
			name:       "owner_id in the body is rejected, not ignored",
			principal:  &auth.Principal{UserID: "user-1"},
			body:       `{"title":"Contrato","kind":"contract","issued_at":"2026-01-02","owner_id":"someone-else"}`,
			wantStatus: http.StatusBadRequest,
			wantInBody: "owner_id",
			// The decoder rejects the body before field validation runs.
			wantNotFound: "VALIDATION_ERROR",
		},
		{
			name:       "title longer than maxTitleLength is rejected",
			principal:  &auth.Principal{UserID: "user-1"},
			body:       `{"title":"` + strings.Repeat("a", maxTitleLength+1) + `","kind":"contract","issued_at":"2026-01-02"}`,
			wantStatus: http.StatusBadRequest,
			wantInBody: "title",
		},
		{
			// maxTitleLength counts characters, not bytes: a title of exactly
			// maxTitleLength multi-byte runes still passes validation and
			// would reach the transaction, so it is pinned with one rune
			// over the limit and an invalid kind to stop before it.
			name:       "title length is counted in characters",
			principal:  &auth.Principal{UserID: "user-1"},
			body:       `{"title":"` + strings.Repeat("ñ", maxTitleLength) + `","kind":"nope","issued_at":"2026-01-02"}`,
			wantStatus: http.StatusBadRequest,
			wantInBody: "kind",
			// Only kind is invalid; the title must not be reported.
			wantNotFound: `"title"`,
		},
		{
			name:       "authenticated caller reaches field validation",
			principal:  &auth.Principal{UserID: "user-1"},
			body:       `{"title":"","kind":"contract","issued_at":"2026-01-02"}`,
			wantStatus: http.StatusBadRequest,
			wantInBody: "VALIDATION_ERROR",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &DocumentHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

			req := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			if tt.principal != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), tt.principal))
			}
			rec := httptest.NewRecorder()

			h.Create(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantInBody) {
				t.Errorf("body = %s, want it to contain %q", rec.Body.String(), tt.wantInBody)
			}
			if tt.wantNotFound != "" && strings.Contains(rec.Body.String(), tt.wantNotFound) {
				t.Errorf("body = %s, want it not to contain %q", rec.Body.String(), tt.wantNotFound)
			}
		})
	}
}

// An offset beyond workflow.MaxListOffset would be rejected by the repository
// anyway; the handler rejects it up front as a 400 instead of reporting the
// repository error as a 500. The handler has no engine or pool: reaching
// either would panic and fail the test.
func TestDocumentHandlerInbox_OffsetAboveMaxIs400(t *testing.T) {
	h := &DocumentHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	req := httptest.NewRequest(http.MethodGet, "/documents/inbox?offset=100001", nil)
	rec := httptest.NewRecorder()

	h.Inbox(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "offset") {
		t.Errorf("body = %s, want it to name the offset field", rec.Body.String())
	}
}

// List bounds offset the same way Inbox does: an unbounded OFFSET makes
// Postgres walk and discard every skipped row, so one request could make the
// database scan the whole table. The handler has no store or pool: reaching
// either would panic and fail the test.
func TestDocumentHandlerList_OffsetAboveMaxIs400(t *testing.T) {
	h := &DocumentHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	req := httptest.NewRequest(http.MethodGet, "/documents?offset=100001", nil)
	rec := httptest.NewRecorder()

	h.List(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "offset") {
		t.Errorf("body = %s, want it to name the offset field", rec.Body.String())
	}
}

// A negative offset never reaches the repository either: request.Validator's
// IntQuery rejects negatives, so the handler answers 400, not the 500 the
// repository's ErrOffsetOutOfRange would otherwise become.
func TestDocumentHandlerInbox_NegativeOffsetIs400(t *testing.T) {
	h := &DocumentHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	req := httptest.NewRequest(http.MethodGet, "/documents/inbox?offset=-1", nil)
	rec := httptest.NewRecorder()

	h.Inbox(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "offset") {
		t.Errorf("body = %s, want it to name the offset field", rec.Body.String())
	}
}

// withURLParam returns r routed with chi URL parameter name=value, as the
// router would deliver it to a handler.
func withURLParam(r *http.Request, name, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(name, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

const testDocID = "0b0c8a3e-6a3b-4b8e-9d2a-2f6f1f6f1f6f"

// UploadURL refuses a type outside allowedUploadTypes (415) and a size it
// would not sign (400 naming size) before it touches the store, the storage
// port or the database: the handler has none of them, so reaching any would
// panic.
func TestDocumentHandlerUploadURL_RejectsBeforeSigning(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantInBody []string
	}{
		{name: "HTML is not an accepted type", body: `{"content_type":"text/html","size":10}`, wantStatus: http.StatusUnsupportedMediaType, wantInBody: append([]string{"UNSUPPORTED_MEDIA_TYPE"}, allowedUploadTypes...)},
		{name: "a missing type is not an accepted type", body: `{"size":10}`, wantStatus: http.StatusUnsupportedMediaType, wantInBody: []string{"UNSUPPORTED_MEDIA_TYPE"}},
		{name: "size 0 is rejected", body: `{"content_type":"application/pdf","size":0}`, wantStatus: http.StatusBadRequest, wantInBody: []string{"VALIDATION_ERROR", `"size"`}},
		{name: "a negative size is rejected", body: `{"content_type":"application/pdf","size":-1}`, wantStatus: http.StatusBadRequest, wantInBody: []string{"VALIDATION_ERROR", `"size"`}},
		{name: "a size above maxUploadBytes is rejected", body: `{"content_type":"application/pdf","size":26214401}`, wantStatus: http.StatusBadRequest, wantInBody: []string{"VALIDATION_ERROR", `"size"`}},
		{name: "an unknown field is rejected", body: `{"content_type":"application/pdf","size":1,"key":"x"}`, wantStatus: http.StatusBadRequest, wantInBody: []string{"key"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &DocumentHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			req := httptest.NewRequest(http.MethodPost, "/documents/x/upload-url", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req = withURLParam(req, "id", testDocID)
			rec := httptest.NewRecorder()

			h.UploadURL(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			for _, want := range tt.wantInBody {
				if !strings.Contains(rec.Body.String(), want) {
					t.Errorf("body = %s, want it to contain %q", rec.Body.String(), want)
				}
			}
		})
	}
}

// AttachFile accepts only a key issued for the document in the URL
// (documents/{id}/uploads/{uuid}); anything else is a 400 before the
// database is touched.
func TestDocumentHandlerAttachFile_RejectsAForeignKey(t *testing.T) {
	prefix := "documents/" + testDocID + "/uploads/"
	tests := []struct {
		name string
		key  string
	}{
		{name: "another document's upload", key: "documents/7e57d0c5-0000-4000-8000-000000000001/uploads/7e57d0c5-0000-4000-8000-000000000002"},
		{name: "the legacy per-document key", key: "documents/" + testDocID},
		{name: "a final file key, which only the job may set", key: "documents/" + testDocID + "/file/7e57d0c5-0000-4000-8000-000000000002"},
		{name: "an empty key", key: ""},
		{name: "the bare prefix", key: prefix},
		{name: "a suffix that is not an issued UUID", key: prefix + "../../other"},
		{name: "a non-canonical UUID", key: prefix + "{7e57d0c5-0000-4000-8000-000000000002}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &DocumentHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			body := `{"key":"` + tt.key + `"}`
			req := httptest.NewRequest(http.MethodPost, "/documents/x/file", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req = withURLParam(req, "id", testDocID)
			rec := httptest.NewRecorder()

			h.AttachFile(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"key"`) {
				t.Errorf("body = %s, want it to name the key field", rec.Body.String())
			}
		})
	}
}

func TestAllowedUploadTypes_AcceptTheDocumentKinds(t *testing.T) {
	for _, want := range []string{"application/pdf", "image/png", "image/jpeg", "text/plain"} {
		found := false
		for _, a := range allowedUploadTypes {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("allowedUploadTypes = %v, missing %s", allowedUploadTypes, want)
		}
	}
}
