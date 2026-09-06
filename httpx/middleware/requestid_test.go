package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/kafeiih/vogel/logger"
)

func TestLoggerRequestID_BridgesChiRequestIDIntoLoggerContext(t *testing.T) {
	var captured string

	handler := chimw.RequestID(LoggerRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = logger.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if captured == "" {
		t.Error("expected logger.RequestIDFromContext to return chi's request ID, got empty string")
	}
}

func TestLoggerRequestID_NoChiRequestID_SetsEmptyString(t *testing.T) {
	var captured string
	seen := false

	handler := LoggerRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		captured = logger.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !seen {
		t.Fatal("next handler was not called")
	}
	if captured != "" {
		t.Errorf("captured = %q, want empty string when chi.RequestID middleware was not mounted", captured)
	}
}
