// Package request provides HTTP request decoding and validation helpers shared
// across handlers: JSON body decoding with size limits and error mapping
// (json.go), localizable user-facing messages (messages.go), and query/URL-
// parameter validation (validate.go).
package request

import (
	"encoding/json"
	"errors"
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

// Decoder decodes JSON request bodies and constructs Validators, both using a
// fixed set of Messages. It is immutable once returned by New — WithMessages
// options only run during construction — so a single Decoder value is safe
// to share and call concurrently from any number of handlers/goroutines.
type Decoder struct {
	messages Messages
}

// New creates a Decoder starting from DefaultMessages, applying opts in
// order. With no options, the returned Decoder behaves identically to the
// package-level JSON/JSONWithLimit/JSONOptional/NewValidator functions.
func New(opts ...Option) *Decoder {
	d := &Decoder{messages: DefaultMessages()}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// defaultDecoder is the Decoder backing every package-level function below.
// It is built once from DefaultMessages and never mutated afterwards.
var defaultDecoder = New()

// JSON decodes the request body into data and returns nil on success.
// The body is limited to defaultMaxBytes. On failure it writes a 400/413 JSON
// response directly and returns the error, so the caller only needs:
//
//	if err := request.JSON(w, r, &req); err != nil {
//	    return
//	}
func JSON(w http.ResponseWriter, r *http.Request, data any) error {
	return defaultDecoder.JSON(w, r, data)
}

// JSONWithLimit decodes the request body into data with a caller-supplied byte
// limit. It behaves like JSON but lets callers override the default limit —
// for example, a bulk-import endpoint that legitimately accepts larger bodies.
// On failure it writes a 400/413 JSON response directly and returns the error.
func JSONWithLimit(w http.ResponseWriter, r *http.Request, data any, maxBytes int64) error {
	return defaultDecoder.JSONWithLimit(w, r, data, maxBytes)
}

// JSONOptional decodes the request body into data like JSON, but treats an
// empty body as success instead of an error: it writes nothing to w, returns
// nil, and leaves data untouched (at its zero value). This is for endpoints
// where the body itself is optional — a PATCH with no fields to update, for
// example — as opposed to a required body that happens to be malformed,
// which is still a 400 exactly like JSON.
func JSONOptional(w http.ResponseWriter, r *http.Request, data any) error {
	return defaultDecoder.JSONOptional(w, r, data)
}

// JSON decodes the request body into data using d's Messages. See the
// package-level JSON for behavior.
func (d *Decoder) JSON(w http.ResponseWriter, r *http.Request, data any) error {
	return d.JSONWithLimit(w, r, data, defaultMaxBytes)
}

// JSONWithLimit decodes the request body into data using d's Messages, with a
// caller-supplied byte limit. See the package-level JSONWithLimit for
// behavior.
func (d *Decoder) JSONWithLimit(w http.ResponseWriter, r *http.Request, data any, maxBytes int64) error {
	return d.decode(w, r, data, maxBytes, false)
}

// JSONOptional decodes the request body into data using d's Messages,
// treating an empty body as success. See the package-level JSONOptional for
// behavior.
func (d *Decoder) JSONOptional(w http.ResponseWriter, r *http.Request, data any) error {
	return d.decode(w, r, data, defaultMaxBytes, true)
}

// decode is the shared implementation behind JSON, JSONWithLimit, and
// JSONOptional: it applies the byte limit, rejects unknown fields, and either
// returns nil, swallows an empty body when allowEmpty is set, or writes a
// decode error response and returns the error.
func (d *Decoder) decode(w http.ResponseWriter, r *http.Request, data any, maxBytes int64, allowEmpty bool) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(data); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return nil
		}
		d.writeDecodeError(w, r, err, maxBytes)
		return err
	}

	return nil
}

func (d *Decoder) writeDecodeError(w http.ResponseWriter, r *http.Request, err error, maxBytes int64) {
	var syntaxErr *json.SyntaxError
	var unmarshalErr *json.UnmarshalTypeError
	var maxBytesErr *http.MaxBytesError

	switch {
	case errors.As(err, &syntaxErr):
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			d.messages.MalformedJSON(syntaxErr.Offset))

	case errors.As(err, &unmarshalErr):
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			d.messages.WrongType(unmarshalErr.Field, unmarshalErr.Type.String()))

	case errors.As(err, &maxBytesErr):
		limitMB := float64(maxBytes) / (1024 * 1024)
		response.Error(w, r, http.StatusRequestEntityTooLarge, response.CodeBodyTooLarge,
			d.messages.BodyTooLarge(limitMB))

	case errors.Is(err, io.EOF):
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			d.messages.EmptyBody)

	case strings.HasPrefix(err.Error(), "json: unknown field"):
		field := strings.TrimPrefix(err.Error(), "json: unknown field ")
		response.Error(w, r, http.StatusBadRequest, response.CodeUnknownField,
			d.messages.UnknownField(field))

	default:
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			d.messages.InvalidBody)
	}
}
