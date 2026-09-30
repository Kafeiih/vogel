package response

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// Error envelope contract. Every error response carries three layers:
//
//   - code: a generic, stable category (NOT_FOUND, CONFLICT, ...). Modules do
//     NOT define their own codes; use the constants below.
//   - error: an optional stable identifier for one specific domain error
//     (SCREAMING_SNAKE_CASE, e.g. CORREO_EN_USO), chosen by the consumer
//     module and attached with WithErrorID. Set it on every business error.
//   - message: human-readable text that may change at any time.
//
// Clients must branch on error (falling back to code), never on message.
// When the error belongs to a single input field, WithField names it.
//
// Standard error codes for API responses.
const (
	CodeNotFound      = "NOT_FOUND"
	CodeInternalError = "INTERNAL_ERROR"
	CodeInvalidJSON   = "INVALID_JSON"
	CodeUnknownField  = "UNKNOWN_FIELD"
	CodeBodyTooLarge  = "BODY_TOO_LARGE"
	// CodeUnprocessableEntity is used with HTTP 422 when the request is well
	// formed but violates a business rule.
	CodeUnprocessableEntity = "UNPROCESSABLE_ENTITY"
	// CodeExportTooLarge is used with HTTP 413 when a requested export exceeds
	// the server's row cap. Distinct from CodeBodyTooLarge, which is about the
	// request body.
	CodeExportTooLarge     = "EXPORT_TOO_LARGE"
	CodeValidationError    = "VALIDATION_ERROR"
	CodeConflict           = "CONFLICT"
	CodeUnauthorized       = "UNAUTHORIZED"
	CodeForbidden          = "FORBIDDEN"
	CodeRateLimited        = "RATE_LIMITED"
	CodeAuthzError         = "AUTHZ_ERROR"
	CodeServiceUnavailable = "SERVICE_UNAVAILABLE"
)

// ErrorResponse is the standard error envelope for API responses.
type ErrorResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Code    string `json:"code"`
	// Error is the stable per-domain-error identifier (e.g. CORREO_EN_USO).
	// Omitted when not set.
	Error string `json:"error,omitempty"`
	// Field names the offending input field, when the error belongs to one.
	// Omitted when not set.
	Field   string `json:"field,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
} //	@name	ErrorResponse

// ValidationErrorResponse is a 400 response with per-field errors.
type ValidationErrorResponse struct {
	Success bool              `json:"success"`
	Message string            `json:"message"`
	Code    string            `json:"code"`
	Fields  map[string]string `json:"fields"`
	TraceID string            `json:"trace_id,omitempty"`
} //	@name	ValidationErrorResponse

// ErrorOption customizes an ErrorResponse built by ErrorWith.
type ErrorOption func(*ErrorResponse)

// WithErrorID sets the stable per-domain-error identifier (the "error" key),
// in SCREAMING_SNAKE_CASE, e.g. "CORREO_EN_USO".
func WithErrorID(id string) ErrorOption {
	return func(e *ErrorResponse) { e.Error = id }
}

// WithField sets the offending input field name (the "field" key).
func WithField(name string) ErrorOption {
	return func(e *ErrorResponse) { e.Field = name }
}

// Error writes the standard error envelope. It is ErrorWith without options.
func Error(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	ErrorWith(w, r, status, code, message)
}

// ErrorWith writes the standard error envelope, applying opts to add a stable
// error identifier and/or the offending field.
func ErrorWith(w http.ResponseWriter, r *http.Request, status int, code, message string, opts ...ErrorOption) {
	resp := ErrorResponse{
		Success: false,
		Message: message,
		Code:    code,
		TraceID: middleware.GetReqID(r.Context()),
	}
	for _, opt := range opts {
		opt(&resp)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("failed to encode error response", "error", err)
	}
}

// ValidationError sends a 400 response containing per-field validation errors.
func ValidationError(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	if err := json.NewEncoder(w).Encode(ValidationErrorResponse{
		Success: false,
		Message: "Validation failed",
		Code:    "VALIDATION_ERROR",
		Fields:  fields,
		TraceID: middleware.GetReqID(r.Context()),
	}); err != nil {
		slog.Error("failed to encode validation error response", "error", err)
	}
}
