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

// UploadFile decides the stored type from the content, not from the client's
// Content-Type header: an HTML body declared as a PDF is rejected with 415
// before the handler touches the store, the storage port or the database
// (the handler has none of them, so reaching any would panic).
func TestDocumentHandlerUploadFile_RejectsDisallowedContent(t *testing.T) {
	tests := []struct {
		name       string
		declared   string
		body       string
		wantInBody string
	}{
		{name: "HTML declared as PDF", declared: "application/pdf", body: "<!DOCTYPE html><html><script>alert(1)</script></html>", wantInBody: "UNSUPPORTED_MEDIA_TYPE"},
		{name: "HTML without a declared type", declared: "", body: "<html><body>hi</body></html>", wantInBody: "UNSUPPORTED_MEDIA_TYPE"},
		{name: "ZIP declared as JPEG", declared: "image/jpeg", body: "PK" + string([]byte{3, 4}) + "rest-of-archive", wantInBody: "UNSUPPORTED_MEDIA_TYPE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &DocumentHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

			req := httptest.NewRequest(http.MethodPut, "/documents/x/file", strings.NewReader(tt.body))
			if tt.declared != "" {
				req.Header.Set("Content-Type", tt.declared)
			}
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", "0b0c8a3e-6a3b-4b8e-9d2a-2f6f1f6f1f6f")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			rec := httptest.NewRecorder()

			h.UploadFile(rec, req)

			if rec.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusUnsupportedMediaType, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantInBody) {
				t.Errorf("body = %s, want it to contain %q", rec.Body.String(), tt.wantInBody)
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
