// Package request provides HTTP request decoding and validation helpers shared
// across handlers: JSON body decoding with size limits and error mapping
// (json.go), and query/URL-parameter validation (validate.go).
package request

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/kafeiih/vogel/httpx/response"
)

// defaultMaxBytes is the default request body size limit for JSON.
//
// This used to be written as 1_048_578 with a "// 1mb" comment in the source
// repositories — off by 2 bytes from the real value of 1 MiB (1_048_576) —
// and the mistake was copied to a second call site in one of them. JSON uses
// the correct value; JSONWithLimit lets a caller pick any other limit.
const defaultMaxBytes = 1_048_576 // 1 MiB

// JSON decodes the request body into data and returns nil on success.
// The body is limited to defaultMaxBytes. On failure it writes a 400/413 JSON
// response directly and returns the error, so the caller only needs:
//
//	if err := request.JSON(w, r, &req); err != nil {
//	    return
//	}
func JSON(w http.ResponseWriter, r *http.Request, data any) error {
	return JSONWithLimit(w, r, data, defaultMaxBytes)
}

// JSONWithLimit decodes the request body into data with a caller-supplied byte
// limit. It behaves like JSON but lets callers override the default limit —
// for example, a bulk-import endpoint that legitimately accepts larger bodies.
// On failure it writes a 400/413 JSON response directly and returns the error.
func JSONWithLimit(w http.ResponseWriter, r *http.Request, data any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(data); err != nil {
		writeDecodeError(w, r, err, maxBytes)
		return err
	}

	return nil
}

func writeDecodeError(w http.ResponseWriter, r *http.Request, err error, maxBytes int64) {
	var syntaxErr *json.SyntaxError
	var unmarshalErr *json.UnmarshalTypeError
	var maxBytesErr *http.MaxBytesError

	switch {
	case errors.As(err, &syntaxErr):
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			fmt.Sprintf("Malformed JSON at position %d", syntaxErr.Offset))

	case errors.As(err, &unmarshalErr):
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			fmt.Sprintf("Wrong type for field %q: expected %s", unmarshalErr.Field, unmarshalErr.Type))

	case errors.As(err, &maxBytesErr):
		limitMB := float64(maxBytes) / (1024 * 1024)
		response.Error(w, r, http.StatusRequestEntityTooLarge, response.CodeBodyTooLarge,
			fmt.Sprintf("Request body exceeds the %.0fMB limit", limitMB))

	case errors.Is(err, io.EOF):
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			"Request body is empty")

	case strings.HasPrefix(err.Error(), "json: unknown field"):
		field := strings.TrimPrefix(err.Error(), "json: unknown field ")
		response.Error(w, r, http.StatusBadRequest, response.CodeUnknownField,
			fmt.Sprintf("Unknown field: %s", field))

	default:
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			"Invalid request body")
	}
}
