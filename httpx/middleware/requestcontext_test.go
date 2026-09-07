package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/kafeiih/vogel/reqctx"
)

func TestRequestContext_ExtractsIPAndUserAgent(t *testing.T) {
	var captured reqctx.RequestInfo
	var ok bool

	handler := RequestContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, ok = reqctx.RequestInfoFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	req.Header.Set("User-Agent", "TestBrowser/1.0")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !ok {
		t.Fatal("RequestInfo not found in context")
	}
	if captured.IP != "192.168.1.1" {
		t.Errorf("IP = %q, want %q", captured.IP, "192.168.1.1")
	}
	if captured.UserAgent != "TestBrowser/1.0" {
		t.Errorf("UserAgent = %q, want %q", captured.UserAgent, "TestBrowser/1.0")
	}
}

func TestRequestContext_RemoteAddrWithoutPort(t *testing.T) {
	var captured reqctx.RequestInfo

	handler := RequestContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = reqctx.RequestInfoFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1" // no port

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if captured.IP != "10.0.0.1" {
		t.Errorf("IP = %q, want %q", captured.IP, "10.0.0.1")
	}
}

func TestRequestContext_BridgesChiRequestIDIntoReqctx(t *testing.T) {
	var captured string

	handler := chimw.RequestID(RequestContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = reqctx.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if captured == "" {
		t.Error("expected reqctx.RequestIDFromContext to return chi's request ID, got empty string")
	}
}

func TestRequestContext_NoChiRequestID_SetsEmptyString(t *testing.T) {
	var captured string
	seen := false

	handler := RequestContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		captured = reqctx.RequestIDFromContext(r.Context())
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

func TestRequestContext_NoInfo_ReturnsFalse(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, ok := reqctx.RequestInfoFromContext(req.Context()); ok {
		t.Error("expected ok=false when RequestContext middleware has not run")
	}
}
