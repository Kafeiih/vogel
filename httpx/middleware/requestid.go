package middleware

import (
	"net/http"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/kafeiih/vogel/logger"
)

// LoggerRequestID bridges chi's per-request ID into the logger package's own
// context key, so logger.Logger.WithContext can pick it up without the
// logger package importing chi. Mount chi's own middleware.RequestID (or
// equivalent) upstream of this middleware.
func LoggerRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := logger.WithRequestID(r.Context(), chimw.GetReqID(r.Context()))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
