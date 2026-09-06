package response

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// SuccessResponse is the standard success envelope for API responses.
type SuccessResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    any    `json:"data"`
} //	@name	SuccessResponse

func JSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(SuccessResponse{
		Success: true,
		Message: "OK",
		Data:    data,
	}); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}

func JSONWithMessage(w http.ResponseWriter, status int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(SuccessResponse{
		Success: true,
		Message: message,
		Data:    data,
	}); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}

func JSONFail(w http.ResponseWriter, status int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(SuccessResponse{
		Success: false,
		Message: message,
		Data:    data,
	}); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}

// ListMeta contains pagination metadata for list responses.
type ListMeta struct {
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
} //	@name	ListMeta

// ListResponse is the envelope for paginated list endpoints.
type ListResponse struct {
	Success bool     `json:"success"`
	Message string   `json:"message"`
	Data    any      `json:"data"`
	Meta    ListMeta `json:"meta"`
} //	@name	ListResponse

// JSONList writes a paginated list response.
func JSONList(w http.ResponseWriter, status int, items any, total, limit, offset int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(ListResponse{
		Success: true,
		Message: "OK",
		Data:    items,
		Meta:    ListMeta{Total: total, Limit: limit, Offset: offset},
	}); err != nil {
		slog.Error("failed to encode list response", "error", err)
	}
}

// JSONListWithMeta writes a paginated list response with a custom meta object.
func JSONListWithMeta(w http.ResponseWriter, status int, items any, meta any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    any    `json:"data"`
		Meta    any    `json:"meta"`
	}{
		Success: true,
		Message: "OK",
		Data:    items,
		Meta:    meta,
	}); err != nil {
		slog.Error("failed to encode list response", "error", err)
	}
}
