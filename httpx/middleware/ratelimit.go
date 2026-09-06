package middleware

import (
	"net/http"

	"github.com/kafeiih/vogel/httpx/response"
)

// RateLimitJSON returns an http.HandlerFunc that writes a JSON 429 response.
// Use with httprate.WithLimitHandler(middleware.RateLimitJSON()).
func RateLimitJSON() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		response.Error(w, r, http.StatusTooManyRequests, response.CodeRateLimited,
			"You have exceeded the request rate limit. Please try again later.")
	}
}
