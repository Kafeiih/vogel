package response_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/httpx/response"
)

// TestCodeExportTooLarge_Value pins the wire value: clients match on it, so it
// is a public API contract.
func TestCodeExportTooLarge_Value(t *testing.T) {
	assert.Equal(t, "EXPORT_TOO_LARGE", response.CodeExportTooLarge)
}

func TestCodeUnprocessableEntity_Value(t *testing.T) {
	assert.Equal(t, "UNPROCESSABLE_ENTITY", response.CodeUnprocessableEntity)
}

func TestError_JSONShapeUnchanged(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	response.Error(rec, req, http.StatusNotFound, response.CodeNotFound, "no existe")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, `{"success":false,"message":"no existe","code":"NOT_FOUND"}`+"\n", rec.Body.String())
}

func TestErrorWith_NoOptionsMatchesError(t *testing.T) {
	a, b := httptest.NewRecorder(), httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	response.Error(a, req, http.StatusConflict, response.CodeConflict, "m")
	response.ErrorWith(b, req, http.StatusConflict, response.CodeConflict, "m")

	assert.Equal(t, a.Body.String(), b.Body.String())
}

func TestErrorWith_Options(t *testing.T) {
	tests := []struct {
		name string
		opts []response.ErrorOption
		want string
	}{
		{"error id", []response.ErrorOption{response.WithErrorID("CORREO_EN_USO")},
			`{"success":false,"message":"m","code":"CONFLICT","error":"CORREO_EN_USO"}`},
		{"field", []response.ErrorOption{response.WithField("correo")},
			`{"success":false,"message":"m","code":"CONFLICT","field":"correo"}`},
		{"both", []response.ErrorOption{response.WithErrorID("CORREO_EN_USO"), response.WithField("correo")},
			`{"success":false,"message":"m","code":"CONFLICT","error":"CORREO_EN_USO","field":"correo"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)

			response.ErrorWith(rec, req, http.StatusConflict, response.CodeConflict, "m", tt.opts...)

			assert.Equal(t, http.StatusConflict, rec.Code)
			assert.Equal(t, tt.want+"\n", rec.Body.String())
		})
	}
}

func TestErrorWith_PropagatesTraceID(t *testing.T) {
	h := middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response.ErrorWith(w, r, http.StatusConflict, response.CodeConflict, "m", response.WithErrorID("X"))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "trace-1")

	h.ServeHTTP(rec, req)

	var got response.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "trace-1", got.TraceID)
	assert.Equal(t, "X", got.Error)
}

func TestValidationError_PropagatesTraceID(t *testing.T) {
	h := middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response.ValidationError(w, r, map[string]string{"a": "required"})
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "trace-2")

	h.ServeHTTP(rec, req)

	var got response.ValidationErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "trace-2", got.TraceID)
}

func TestValidationError_NoTraceIDWithoutMiddleware(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	response.ValidationError(rec, req, map[string]string{"a": "required"})

	assert.NotContains(t, rec.Body.String(), "trace_id")
}
