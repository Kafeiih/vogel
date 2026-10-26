package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/request"
	"github.com/kafeiih/vogel/workflow"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/application/process"
	domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
)

type stubProcessService struct {
	err       error
	gotActor  string
	gotValues map[string]any
	published *domain.ProcessSpec
}

func (s *stubProcessService) Publish(_ context.Context, spec domain.ProcessSpec) (*domain.Process, error) {
	s.published = &spec
	if s.err != nil {
		return nil, s.err
	}
	return &domain.Process{Spec: spec, Definition: workflow.Definition{Name: spec.Name, Version: 3}}, nil
}

func (s *stubProcessService) Latest(name string) (*domain.Process, bool) {
	if name != "consulta" {
		return nil, false
	}
	return &domain.Process{Spec: domain.ProcessSpec{Name: name}, Definition: workflow.Definition{Name: name, Version: 2}}, true
}

func (s *stubProcessService) Start(_ context.Context, actor, _, _ string, values map[string]any) (*workflow.Case, error) {
	s.gotActor, s.gotValues = actor, values
	if s.err != nil {
		return nil, s.err
	}
	return &workflow.Case{ID: uuid.New(), Definition: "consulta", Version: 1, State: "respuesta", Status: workflow.StatusOpen}, nil
}

func (s *stubProcessService) Inbox(_ context.Context, actor string) ([]workflow.Case, error) {
	s.gotActor = actor
	return []workflow.Case{{ID: uuid.New(), State: "respuesta"}}, s.err
}

func (s *stubProcessService) View(_ context.Context, actor string, id uuid.UUID) (*process.CaseView, error) {
	s.gotActor = actor
	if s.err != nil {
		return nil, s.err
	}
	return &process.CaseView{Case: &workflow.Case{ID: id}, Values: map[string]any{"tema": "general"}}, nil
}

func (s *stubProcessService) Claim(_ context.Context, actor string, id uuid.UUID) (*workflow.Case, error) {
	s.gotActor = actor
	if s.err != nil {
		return nil, s.err
	}
	return &workflow.Case{ID: id, AssignedTo: actor}, nil
}

func (s *stubProcessService) Submit(_ context.Context, actor string, id uuid.UUID, _ string, values map[string]any) (*workflow.Case, error) {
	s.gotActor, s.gotValues = actor, values
	if s.err != nil {
		return nil, s.err
	}
	return &workflow.Case{ID: id, State: "cerrada", Status: workflow.StatusClosed}, nil
}

func newProcessRouterWithLogger(svc ProcessService, logger *slog.Logger) http.Handler {
	h := NewProcessHandler(svc, logger)
	r := chi.NewRouter()
	r.Post("/processes", h.Publish)
	r.Get("/processes/{name}", h.Latest)
	r.Post("/processes/{name}/cases", h.Start)
	r.Get("/inbox", h.Inbox)
	r.Get("/cases/{id}", h.View)
	r.Post("/cases/{id}/claim", h.Claim)
	r.Post("/cases/{id}/submit", h.Submit)
	return r
}

func serveProcess(t *testing.T, svc ProcessService, method, path, actor, body string) (int, map[string]any) {
	t.Helper()
	return serveProcessWithLogger(t, svc, slog.New(slog.NewTextHandler(io.Discard, nil)), method, path, actor, body)
}

func serveProcessWithLogger(t *testing.T, svc ProcessService, logger *slog.Logger, method, path, actor, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if actor != "" {
		req = req.WithContext(auth.WithPrincipal(req.Context(), &auth.Principal{UserID: actor}))
	}
	rec := httptest.NewRecorder()
	newProcessRouterWithLogger(svc, logger).ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

const validSpec = `{"name":"consulta","nodes":[{"id":"a","start":true},{"id":"b","terminal":true}],"transitions":[{"from":"a","to":"b","action":"go"}]}`

func TestProcessHandler_ActorComesFromThePrincipal(t *testing.T) {
	svc := &stubProcessService{}
	code, _ := serveProcess(t, svc, http.MethodPost, "/processes/consulta/cases", "user-1", `{"action":"enviar","values":{"tema":"general"}}`)
	if code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", code)
	}
	if svc.gotActor != "user-1" || svc.gotValues["tema"] != "general" {
		t.Fatalf("service got actor %q values %v", svc.gotActor, svc.gotValues)
	}
}

func TestProcessHandler_RejectsAMissingPrincipal(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/processes/consulta/cases"},
		{http.MethodGet, "/inbox"},
		{http.MethodGet, "/cases/" + uuid.NewString()},
		{http.MethodPost, "/cases/" + uuid.NewString() + "/claim"},
		{http.MethodPost, "/cases/" + uuid.NewString() + "/submit"},
	} {
		code, _ := serveProcess(t, &stubProcessService{}, tc.method, tc.path, "", `{}`)
		if code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", tc.method, tc.path, code)
		}
	}
}

func TestProcessHandler_MapsServiceErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"form", &domain.FormError{Fields: request.FieldErrors{"correo": "is required"}}, http.StatusBadRequest},
		{"process not found", domain.ErrProcessNotFound, http.StatusNotFound},
		{"case not found", workflow.ErrCaseNotFound, http.StatusNotFound},
		{"not holder", domain.ErrNotHolder, http.StatusForbidden},
		{"not claimant", domain.ErrNotClaimant, http.StatusForbidden},
		{"no access", domain.ErrNoAccess, http.StatusForbidden},
		{"already assigned", workflow.ErrAlreadyAssigned, http.StatusConflict},
		{"closed", workflow.ErrCaseClosed, http.StatusConflict},
		{"invalid transition", workflow.ErrInvalidTransition, http.StatusConflict},
		{"unknown", errors.New("db down"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := serveProcess(t, &stubProcessService{err: tc.err}, http.MethodPost,
				"/cases/"+uuid.NewString()+"/submit", "user-1", `{"action":"responder","values":{}}`)
			if code != tc.want {
				t.Fatalf("status = %d, want %d (%v)", code, tc.want, body)
			}
			if tc.want == http.StatusInternalServerError && strings.Contains(body["message"].(string), "db down") {
				t.Fatalf("500 leaks the cause: %v", body)
			}
		})
	}
}

func TestProcessHandler_FormErrorListsFields(t *testing.T) {
	svc := &stubProcessService{err: &domain.FormError{Fields: request.FieldErrors{"correo": "must be a single email address"}}}
	_, body := serveProcess(t, svc, http.MethodPost, "/processes/consulta/cases", "user-1", `{"action":"enviar","values":{}}`)
	fields, _ := body["fields"].(map[string]any)
	if fields["correo"] != "must be a single email address" {
		t.Fatalf("fields = %v", body["fields"])
	}
}

func TestProcessHandler_RejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"unknown body field", http.MethodPost, "/processes/consulta/cases", `{"action":"enviar","extra":1}`, http.StatusBadRequest},
		{"malformed body", http.MethodPost, "/cases/" + uuid.NewString() + "/submit", `{`, http.StatusBadRequest},
		{"bad case id", http.MethodGet, "/cases/not-a-uuid", ``, http.StatusNotFound},
		{"spec with a version", http.MethodPost, "/processes", `{"name":"x","version":2,"nodes":[],"transitions":[]}`, http.StatusBadRequest},
		{"oversized body", http.MethodPost, "/processes", `{"name":"` + strings.Repeat("x", maxProcessBodyBytes) + `"}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := serveProcess(t, &stubProcessService{}, tc.method, tc.path, "user-1", tc.body)
			if code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
		})
	}
}

func TestProcessHandler_Publish(t *testing.T) {
	svc := &stubProcessService{}
	code, body := serveProcess(t, svc, http.MethodPost, "/processes", "admin-1", validSpec)
	if code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%v)", code, body)
	}
	data := body["data"].(map[string]any)
	if data["name"] != "consulta" || data["version"] != float64(3) {
		t.Fatalf("data = %v", data)
	}

	svc = &stubProcessService{err: &domain.CompileError{Err: errors.New(`field "x": unknown type "rut"`)}}
	code, body = serveProcess(t, svc, http.MethodPost, "/processes", "admin-1", validSpec)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body["message"].(string), "rut") {
		t.Fatalf("compile error: status = %d body = %v, want 422 naming the cause", code, body)
	}

	svc = &stubProcessService{err: fmt.Errorf("publish %q: store: %w", "consulta", domain.ErrPublishConflict)}
	code, body = serveProcess(t, svc, http.MethodPost, "/processes", "admin-1", validSpec)
	if code != http.StatusConflict || body["code"] != response.CodeConflict {
		t.Fatalf("concurrent publish: status = %d body = %v, want 409 CONFLICT", code, body)
	}

	svc = &stubProcessService{err: errors.New("duplicate key")}
	code, _ = serveProcess(t, svc, http.MethodPost, "/processes", "admin-1", validSpec)
	if code != http.StatusInternalServerError {
		t.Fatalf("store error: status = %d, want 500", code)
	}
}

func TestProcessHandler_Latest(t *testing.T) {
	code, body := serveProcess(t, &stubProcessService{}, http.MethodGet, "/processes/consulta", "user-1", ``)
	if code != http.StatusOK || body["data"].(map[string]any)["version"] != float64(2) {
		t.Fatalf("status = %d body = %v", code, body)
	}
	code, _ = serveProcess(t, &stubProcessService{}, http.MethodGet, "/processes/nope", "user-1", ``)
	if code != http.StatusNotFound {
		t.Fatalf("unknown process status = %d, want 404", code)
	}
}

func TestProcessHandler_InboxViewClaimSubmit(t *testing.T) {
	id := uuid.NewString()
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodGet, "/inbox", ``, http.StatusOK},
		{http.MethodGet, "/cases/" + id, ``, http.StatusOK},
		{http.MethodPost, "/cases/" + id + "/claim", ``, http.StatusOK},
		{http.MethodPost, "/cases/" + id + "/submit", `{"action":"responder","values":{"respuesta":"ok"}}`, http.StatusOK},
	} {
		svc := &stubProcessService{}
		code, body := serveProcess(t, svc, tc.method, tc.path, "user-2", tc.body)
		if code != tc.want || svc.gotActor != "user-2" {
			t.Errorf("%s %s status = %d actor = %q body = %v", tc.method, tc.path, code, svc.gotActor, body)
		}
	}
}

// captureLogger returns a JSON logger writing to a buffer, and a func that
// decodes every record written so far.
func captureLogger(t *testing.T) (*slog.Logger, func() []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))
	return l, func() []map[string]any {
		var records []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("decode log %q: %v", line, err)
			}
			records = append(records, rec)
		}
		return records
	}
}

func TestProcessHandler_RejectionsLogSecurityEvent(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		err              error
		wantCode         string
		wantFields       []any
	}{
		{"form error", "/processes/consulta/cases", `{"action":"enviar","values":{}}`,
			&domain.FormError{Fields: request.FieldErrors{"tema": "is required", "correo": "is required"}},
			response.CodeValidationError, []any{"correo", "tema"}},
		{"malformed body", "/cases/" + uuid.NewString() + "/submit", `{`, nil, response.CodeInvalidJSON, nil},
		{"unknown body field", "/processes/consulta/cases", `{"action":"enviar","extra":1}`, nil, response.CodeInvalidJSON, nil},
		{"spec with a version", "/processes", `{"name":"x","version":2,"nodes":[],"transitions":[]}`, nil, response.CodeInvalidJSON, nil},
		{"oversized spec", "/processes", `{"name":"` + strings.Repeat("x", maxProcessBodyBytes) + `"}`, nil, response.CodeInvalidJSON, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, records := captureLogger(t)
			code, body := serveProcessWithLogger(t, &stubProcessService{err: tc.err}, l, http.MethodPost, tc.path, "user-1", tc.body)
			if code != http.StatusBadRequest || body["code"] != tc.wantCode {
				t.Fatalf("status = %d body = %v, want 400 %s", code, body, tc.wantCode)
			}
			recs := records()
			if len(recs) != 1 {
				t.Fatalf("logged %d records, want 1: %v", len(recs), recs)
			}
			rec := recs[0]
			if rec["level"] != "WARN" || rec["msg"] != "request validation failed" {
				t.Fatalf("record = %v, want the WARN validation event", rec)
			}
			if rec["status"] != float64(http.StatusBadRequest) || rec["code"] != tc.wantCode || rec["path"] != tc.path {
				t.Fatalf("record = %v, want status 400, code %s, path %s", rec, tc.wantCode, tc.path)
			}
			got, logged := rec["fields"]
			if (tc.wantFields == nil) == logged || (logged && fmt.Sprint(got) != fmt.Sprint(tc.wantFields)) {
				t.Fatalf("fields = %v (logged %t), want %v", got, logged, tc.wantFields)
			}
		})
	}
}

func TestProcessHandler_NonValidationErrorsLogNoValidationEvent(t *testing.T) {
	l, records := captureLogger(t)
	code, _ := serveProcessWithLogger(t, &stubProcessService{err: domain.ErrNotHolder}, l, http.MethodPost,
		"/cases/"+uuid.NewString()+"/submit", "user-1", `{"action":"responder","values":{}}`)
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
	if recs := records(); len(recs) != 0 {
		t.Fatalf("logged %v, want nothing", recs)
	}
}
