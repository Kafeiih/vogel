package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct {
	err error
}

func (f *fakePinger) Ping(context.Context) error {
	return f.err
}

// --- /livez ---

func TestLive_Always200(t *testing.T) {
	h := NewHealthHandler(&fakePinger{})

	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	rec := httptest.NewRecorder()
	h.Live(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	data := body["data"].(map[string]any)
	if data["status"] != "alive" {
		t.Errorf("status = %v, want %q", data["status"], "alive")
	}
}

// --- /healthz ---

func TestCheck_Healthy_Returns200(t *testing.T) {
	h := NewHealthHandler(&fakePinger{err: nil})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.Check(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	data := body["data"].(map[string]any)
	if data["status"] != "ok" {
		t.Errorf("status = %v, want %q", data["status"], "ok")
	}
	checks := data["checks"].(map[string]any)
	db := checks["database"].(map[string]any)
	if db["status"] != "up" {
		t.Errorf("db status = %v, want %q", db["status"], "up")
	}
}

func TestCheck_DBDown_Returns503(t *testing.T) {
	h := NewHealthHandler(&fakePinger{err: errors.New("connection refused")})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.Check(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	data := body["data"].(map[string]any)
	if data["status"] != "degraded" {
		t.Errorf("status = %v, want %q", data["status"], "degraded")
	}
	checks := data["checks"].(map[string]any)
	db := checks["database"].(map[string]any)
	if db["status"] != "down" {
		t.Errorf("db status = %v, want %q", db["status"], "down")
	}
	if db["error"] != "connection refused" {
		t.Errorf("error = %v, want %q", db["error"], "connection refused")
	}
}

// --- /readyz ---

func TestReady_Healthy_Returns200(t *testing.T) {
	h := NewHealthHandler(&fakePinger{err: nil})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	data := body["data"].(map[string]any)
	if data["status"] != "ready" {
		t.Errorf("status = %v, want %q", data["status"], "ready")
	}
}

func TestReady_DBDown_Returns503(t *testing.T) {
	h := NewHealthHandler(&fakePinger{err: errors.New("timeout")})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	data := body["data"].(map[string]any)
	if data["status"] != "not_ready" {
		t.Errorf("status = %v, want %q", data["status"], "not_ready")
	}
}
