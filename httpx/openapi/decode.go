package openapi

import (
	"bytes"
	"io"
	"net/http"

	"github.com/kafeiih/vogel/request"
)

// Decode returns a middleware that runs check on the request body before the
// generated strict server decodes it, so a malformed body is rejected with
// the project's own responses (status, code and messages) instead of the
// generated decoder's. check writes the rejection itself and returns false;
// when it returns true, the body is rewound so the generated decoder reads it
// from the start.
func Decode(check func(w http.ResponseWriter, r *http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := r.Body
			var read bytes.Buffer
			r.Body = readCloser{io.TeeReader(body, &read), body}
			if !check(w, r) {
				return
			}
			r.Body = readCloser{io.MultiReader(&read, body), body}
			next.ServeHTTP(w, r)
		})
	}
}

// DecodeJSON returns a Decode middleware that checks the body with vogel's
// JSON decoder into a fresh T: Content-Type, size limit, unknown fields,
// trailing data and type errors answer 400/413/415 exactly as request.JSON
// does. d is the decoder to use (for its Messages or options); nil uses
// request.JSON.
func DecodeJSON[T any](d *request.Decoder) func(http.Handler) http.Handler {
	return Decode(func(w http.ResponseWriter, r *http.Request) bool {
		var body T
		if d == nil {
			return request.JSON(w, r, &body) == nil
		}
		return d.JSON(w, r, &body) == nil
	})
}

type readCloser struct {
	io.Reader
	io.Closer
}
