package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestInfoMiddleware_ExtractsIPAndUserAgent(t *testing.T) {
	var captured *RequestInfo

	handler := RequestInfoMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = RequestInfoFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	req.Header.Set("User-Agent", "TestBrowser/1.0")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if captured == nil {
		t.Fatal("RequestInfo not found in context")
	}
	if captured.IP != "192.168.1.1" {
		t.Errorf("IP = %q, want %q", captured.IP, "192.168.1.1")
	}
	if captured.UserAgent != "TestBrowser/1.0" {
		t.Errorf("UserAgent = %q, want %q", captured.UserAgent, "TestBrowser/1.0")
	}
}

func TestRequestInfoMiddleware_RemoteAddrWithoutPort(t *testing.T) {
	var captured *RequestInfo

	handler := RequestInfoMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = RequestInfoFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1" // no port

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if captured.IP != "10.0.0.1" {
		t.Errorf("IP = %q, want %q", captured.IP, "10.0.0.1")
	}
}

func TestRequestInfoFromContext_NoInfo_ReturnsNil(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	info := RequestInfoFromContext(req.Context())
	if info != nil {
		t.Error("expected nil when no RequestInfo in context")
	}
}
