package middleware

import "net/http"

// SecurityHeaders adds common HTTP security headers to every response.
//
// It also sets Cache-Control: no-store (and Pragma: no-cache for HTTP/1.0
// intermediaries) so that API responses, which are authenticated and
// per-user by default, are never retained by a browser or shared cache.
// Every header is set BEFORE next.ServeHTTP runs, so a handler that serves
// genuinely cacheable content (a public asset, a versioned schema) can
// override the default by setting its own Cache-Control, and deleting
// Pragma, before writing its response.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "0")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")

		next.ServeHTTP(w, r)
	})
}
