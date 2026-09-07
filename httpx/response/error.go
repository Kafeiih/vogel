package response

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// Standard error codes for API responses.
// Modules do NOT define their own codes — business errors are distinguished
// by the message field, not the code.
const (
	CodeNotFound           = "NOT_FOUND"
	CodeInternalError      = "INTERNAL_ERROR"
	CodeInvalidJSON        = "INVALID_JSON"
	CodeUnknownField       = "UNKNOWN_FIELD"
	CodeBodyTooLarge       = "BODY_TOO_LARGE"
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
	TraceID string `json:"trace_id,omitempty"`
} //	@name	ErrorResponse

// ValidationErrorResponse is a 400 response with per-field errors.
type ValidationErrorResponse struct {
	Success bool              `json:"success"`
	Message string            `json:"message"`
	Code    string            `json:"code"`
	Fields  map[string]string `json:"fields"`
} //	@name	ValidationErrorResponse

func Error(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	traceID := middleware.GetReqID(r.Context())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(ErrorResponse{
		Success: false,
		Message: message,
		Code:    code,
		TraceID: traceID,
	}); err != nil {
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
	}); err != nil {
		slog.Error("failed to encode validation error response", "error", err)
	}
}
