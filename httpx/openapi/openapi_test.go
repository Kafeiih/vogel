package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/httpx/openapi"
	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/request"
)

func TestWriteError_WritesTheResponseTheErrorCarries(t *testing.T) {
	cause := errors.New("entry missing")
	err := openapi.Fail(cause, func(w http.ResponseWriter, r *http.Request) {
		response.Error(w, r, http.StatusNotFound, response.CodeNotFound, "Entry not found")
	})
	rec := httptest.NewRecorder()

	written := openapi.WriteError(rec, httptest.NewRequest(http.MethodGet, "/", nil), wrap(err))

	assert.True(t, written)
	assert.ErrorIs(t, err, cause)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.JSONEq(t, `{"success":false,"message":"Entry not found","code":"NOT_FOUND"}`, rec.Body.String())
}

func wrap(err error) error { return errors.Join(errors.New("wrapped"), err) }

func TestWriteError_IgnoresErrorsWithoutAResponse(t *testing.T) {
	rec := httptest.NewRecorder()

	written := openapi.WriteError(rec, httptest.NewRequest(http.MethodGet, "/", nil), errors.New("boom"))

	assert.False(t, written)
	assert.Zero(t, rec.Body.Len())
}

func TestRespond_AnswersWithVogelsErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()

	openapi.WriteError(rec, httptest.NewRequest(http.MethodGet, "/", nil),
		openapi.Respond(http.StatusBadRequest, response.CodeValidationError, "offset out of range", response.WithField("offset")))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.JSONEq(t, `{"success":false,"message":"offset out of range","code":"VALIDATION_ERROR","field":"offset"}`, rec.Body.String())
}

func TestInvalid_AnswersWithTheValidatorsFieldErrors(t *testing.T) {
	v := request.NewValidator()
	v.AddError("limit", "limit must be an integer")
	rec := httptest.NewRecorder()

	err := openapi.Invalid(v)
	openapi.WriteError(rec, httptest.NewRequest(http.MethodGet, "/", nil), err)

	assert.EqualError(t, err, "request rejected")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.JSONEq(t, `{"success":false,"message":"Validation failed","code":"VALIDATION_ERROR","fields":{"limit":"limit must be an integer"}}`, rec.Body.String())
}

func TestResponseErrorHandler_LogsUnknownErrorsAndAnswers500(t *testing.T) {
	var logs bytes.Buffer
	handle := openapi.ResponseErrorHandler(slog.New(slog.NewJSONHandler(&logs, nil)))
	rec := httptest.NewRecorder()

	handle(rec, httptest.NewRequest(http.MethodGet, "/audit", nil), errors.New("db down"))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"success":false,"message":"Internal server error","code":"INTERNAL_ERROR"}`, rec.Body.String())
	assert.Contains(t, logs.String(), `"error":"db down"`)
	assert.Contains(t, logs.String(), `"path":"/audit"`)
}

func TestResponseErrorHandler_WritesTheCarriedResponseWithoutLogging(t *testing.T) {
	var logs bytes.Buffer
	handle := openapi.ResponseErrorHandler(slog.New(slog.NewJSONHandler(&logs, nil)))
	rec := httptest.NewRecorder()

	handle(rec, httptest.NewRequest(http.MethodGet, "/", nil), openapi.Respond(http.StatusConflict, response.CodeConflict, "taken"))

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Empty(t, logs.String())
}

func TestRequestErrorHandler_Answers400InvalidJSON(t *testing.T) {
	rec := httptest.NewRecorder()

	openapi.RequestErrorHandler(rec, httptest.NewRequest(http.MethodPost, "/", nil), errors.New("unexpected EOF"))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.JSONEq(t, `{"success":false,"message":"Invalid request body","code":"INVALID_JSON"}`, rec.Body.String())
}

type ctxKey struct{}

func TestRequest_ReturnsTheRequestTheMiddlewareSaw(t *testing.T) {
	var got *http.Request
	h := openapi.RequestInContext(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = openapi.Request(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/audit?limit=5", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, "user-1"))

	h.ServeHTTP(httptest.NewRecorder(), req)

	require.NotNil(t, got)
	assert.Equal(t, "5", got.URL.Query().Get("limit"))
	assert.Equal(t, "user-1", got.Context().Value(ctxKey{}))
	assert.Same(t, got, openapi.Request(got.Context()), "the stored request carries the key itself")
}

func TestRequest_PanicsWithoutTheMiddleware(t *testing.T) {
	assert.PanicsWithValue(t, "openapi: RequestInContext middleware is not installed", func() {
		openapi.Request(context.Background())
	})
}

func TestServeSpec_ServesTheRawDocument(t *testing.T) {
	spec := []byte("openapi: 3.1.0\n")
	rec := httptest.NewRecorder()

	openapi.ServeSpec(spec).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/yaml", rec.Header().Get("Content-Type"))
	assert.Equal(t, "openapi: 3.1.0\n", rec.Body.String())
}

type startCase struct {
	Name string `json:"name"`
}

// decodeAgain is what the generated strict server does after the middleware.
func decodeAgain(t *testing.T, got *startCase) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(got))
		w.WriteHeader(http.StatusNoContent)
	})
}

func jsonRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/cases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestDecodeJSON_RewindsAValidBodyForTheGeneratedDecoder(t *testing.T) {
	var got startCase
	rec := httptest.NewRecorder()

	openapi.DecodeJSON[startCase](nil)(decodeAgain(t, &got)).ServeHTTP(rec, jsonRequest(`{"name":"leave"}`))

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "leave", got.Name)
}

func TestDecodeJSON_AnswersVogelsRejections(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next must not run") })
	for name, tc := range map[string]struct {
		req    *http.Request
		status int
		code   string
	}{
		"unknown field": {jsonRequest(`{"name":"x","extra":1}`), http.StatusBadRequest, response.CodeUnknownField},
		"wrong media":   {httptest.NewRequest(http.MethodPost, "/cases", strings.NewReader(`{}`)), http.StatusUnsupportedMediaType, response.CodeUnsupportedMediaType},
		"too large":     {jsonRequest(`{"name":"` + strings.Repeat("a", 2<<20) + `"}`), http.StatusRequestEntityTooLarge, response.CodeBodyTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()

			openapi.DecodeJSON[startCase](nil)(next).ServeHTTP(rec, tc.req)

			assert.Equal(t, tc.status, rec.Code)
			var body response.ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tc.code, body.Code)
		})
	}
}

func TestDecodeJSON_UsesTheGivenDecoder(t *testing.T) {
	var got startCase
	req := httptest.NewRequest(http.MethodPost, "/cases", strings.NewReader(`{"name":"plain"}`))
	rec := httptest.NewRecorder()

	openapi.DecodeJSON[startCase](request.New(request.AllowAnyContentType()))(decodeAgain(t, &got)).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "plain", got.Name)
}

func TestDecode_RunsACustomCheckAndRewinds(t *testing.T) {
	var seen string
	check := func(w http.ResponseWriter, r *http.Request) bool {
		b, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		seen = string(b)
		return true
	}
	var got startCase
	rec := httptest.NewRecorder()

	openapi.Decode(check)(decodeAgain(t, &got)).ServeHTTP(rec, jsonRequest(`{"name":"raw"}`))

	assert.Equal(t, `{"name":"raw"}`, seen)
	assert.Equal(t, "raw", got.Name)
}

func TestDecode_StopsWhenTheCheckRejects(t *testing.T) {
	check := func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusTeapot)
		return false
	}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next must not run") })
	rec := httptest.NewRecorder()

	openapi.Decode(check)(next).ServeHTTP(rec, jsonRequest(`{}`))

	assert.Equal(t, http.StatusTeapot, rec.Code)
}
