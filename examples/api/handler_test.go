package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
