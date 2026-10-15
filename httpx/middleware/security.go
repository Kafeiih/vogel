package middleware

import "net/http"

// SecurityHeaders adds common HTTP security headers to every response.
//
// It also sets Cache-Control: private, no-store (and Pragma: no-cache for
// HTTP/1.0 intermediaries) so that API responses, which are authenticated
// and per-user by default, are never retained by a browser and never by a
// shared cache.
//
// Cross-Origin-Opener-Policy: same-origin isolates the browsing context from
// cross-origin windows, and Cross-Origin-Resource-Policy: same-origin stops
// other origins from embedding a response (an <img>, <script> or no-cors
// fetch from a foreign page is blocked by the browser).
//
// Every header is set BEFORE next.ServeHTTP runs, so a handler that serves
// genuinely cacheable or embeddable content (a public asset, a versioned
// schema) can override the defaults by setting its own Cache-Control (and
// deleting Pragma) or its own Cross-Origin-Resource-Policy (for example
// "cross-origin") before writing its response.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "0")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Pragma", "no-cache")

		next.ServeHTTP(w, r)
	})
}
