// Package request provides HTTP request decoding and validation helpers shared
// across handlers: JSON body decoding with a Content-Type check, size limits
// and error mapping (json.go), localizable user-facing messages
// (messages.go), and query/URL-parameter validation (validate.go).
package request

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
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
//
// Build it with New. A Decoder that did not come from New — a zero value or a
// nil pointer — is still safe to use: it falls back to DefaultMessages
// instead of calling a nil message func.
type Decoder struct {
	messages Messages
	// allowAnyContentType disables the Content-Type gate; see
	// AllowAnyContentType.
	allowAnyContentType bool
	// configured is set by New; it tells a real configuration apart from the
	// zero value, whose Messages would hold nil funcs.
	configured bool
}

// New creates a Decoder starting from DefaultMessages, applying opts in
// order. With no options, the returned Decoder behaves identically to the
// package-level JSON/JSONWithLimit/JSONOptional/NewValidator functions.
func New(opts ...Option) *Decoder {
	d := &Decoder{messages: DefaultMessages(), configured: true}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// AllowAnyContentType disables the Content-Type check for a Decoder built
// with New: every body is decoded whatever media type the client declares,
// or declares none, exactly as this package behaved before the check
// existed. It is an escape hatch for a consumer with legacy clients that
// cannot be fixed to send application/json; every other rule — the byte
// limit, unknown-field rejection, the single-value rule — still applies.
func AllowAnyContentType() Option {
	return func(d *Decoder) {
		d.allowAnyContentType = true
	}
}

// defaultDecoder is the Decoder backing every package-level function below.
// It is built once from DefaultMessages and never mutated afterwards.
var defaultDecoder = New()

// JSON decodes the request body into data and returns nil on success.
// The body is limited to defaultMaxBytes, must be declared as application/json
// or application/*+json by the Content-Type header, and must hold exactly one
// JSON value. On failure it writes a 400/413/415 JSON response directly and
// returns the error, so the caller only needs:
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
// On failure it writes a 400/413/415 JSON response directly and returns the error.
func JSONWithLimit(w http.ResponseWriter, r *http.Request, data any, maxBytes int64) error {
	return defaultDecoder.JSONWithLimit(w, r, data, maxBytes)
}

// JSONOptional decodes the request body into data like JSON, but treats an
// empty body as success instead of an error: it writes nothing to w, returns
// nil, and leaves data exactly as the caller passed it — it is not reset, so
// a destination reused across requests keeps its previous values. Pass a
// fresh (zero) value to get defaults for an absent body. This is for endpoints
// where the body itself is optional — a PATCH with no fields to update, for
// example — as opposed to a required body that happens to be malformed,
// which is still a 400 exactly like JSON. An empty body may also omit the
// Content-Type header; a non-empty body must declare a JSON media type or
// the request is a 415 exactly like JSON.
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
// JSONOptional: it applies the byte limit, checks the Content-Type, rejects
// unknown fields, requires the body to hold exactly one JSON value, and either
// returns nil, swallows an empty body when allowEmpty is set, or writes an
// error response and returns the error.
func (d *Decoder) decode(w http.ResponseWriter, r *http.Request, data any, maxBytes int64, allowEmpty bool) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if d.checksContentType() {
		if err := d.checkContentType(w, r, decoder, allowEmpty); err != nil {
			if errors.Is(err, errAbsentOptionalBody) {
				return nil
			}
			return err
		}
	}

	if err := decoder.Decode(data); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return nil
		}
		d.writeDecodeError(w, r, err, maxBytes)
		return err
	}

	// Exactly one value: anything but EOF after it — a second value, stray
	// bytes, or the byte limit tripping on the extra bytes — is an error.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			d.writeDecodeError(w, r, err, maxBytes)
			return err
		}
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			d.msgs().TrailingData)
		return errTrailingData
	}

	return nil
}

var (
	// errUnsupportedMediaType is returned by the decode entry points after a
	// 415 was written.
	errUnsupportedMediaType = errors.New("request: unsupported media type")
	// errTrailingData is returned by the decode entry points after a 400 was
	// written for data following the first JSON value.
	errTrailingData = errors.New("request: data after the JSON value")
	// errAbsentOptionalBody is internal to decode: checkContentType reports it
	// when JSONOptional received neither a Content-Type nor a body, which is
	// a success that must skip decoding.
	errAbsentOptionalBody = errors.New("request: absent optional body")
)

// checksContentType reports whether d enforces a JSON Content-Type. A nil or
// zero-value Decoder enforces it, like New() with no options.
func (d *Decoder) checksContentType() bool {
	return d == nil || !d.allowAnyContentType
}

// checkContentType enforces the media-type gate. It returns nil when the body
// may be decoded, errAbsentOptionalBody when allowEmpty is set and the
// request carries neither a Content-Type nor a body, and
// errUnsupportedMediaType — after writing the 415 — otherwise.
//
// The header is only optional for an absent body: a client that sends
// nothing has nothing to describe. decoder.Token is what decides whether the
// body is absent, so whitespace-only bodies count as empty exactly as they do
// for JSONOptional with a proper header, and the Content-Length header —
// which a chunked request does not carry — is never consulted.
func (d *Decoder) checkContentType(w http.ResponseWriter, r *http.Request, decoder *json.Decoder, allowEmpty bool) error {
	contentType := r.Header.Get("Content-Type")
	if contentType == "" && allowEmpty {
		if _, err := decoder.Token(); errors.Is(err, io.EOF) {
			return errAbsentOptionalBody
		}
	} else if isJSONMediaType(contentType) {
		return nil
	}

	response.Error(w, r, http.StatusUnsupportedMediaType, response.CodeUnsupportedMediaType,
		d.msgs().UnsupportedMediaType)
	return errUnsupportedMediaType
}

// isJSONMediaType reports whether contentType names application/json or any
// application/*+json structured-syntax type (application/vnd.api+json,
// application/problem+json, ...). Parameters such as charset are ignored; an
// unparseable header is not JSON.
func isJSONMediaType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "application/json" ||
		(strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json"))
}

// msgs returns the Messages d was built with, or the package defaults when d
// did not come from New.
func (d *Decoder) msgs() *Messages {
	if d == nil || !d.configured {
		return &defaultDecoder.messages
	}
	return &d.messages
}

func (d *Decoder) writeDecodeError(w http.ResponseWriter, r *http.Request, err error, maxBytes int64) {
	m := d.msgs()
	var syntaxErr *json.SyntaxError
	var unmarshalErr *json.UnmarshalTypeError
	var maxBytesErr *http.MaxBytesError

	switch {
	case errors.As(err, &syntaxErr):
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			m.MalformedJSON(syntaxErr.Offset))

	case errors.As(err, &unmarshalErr):
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			m.WrongType(unmarshalErr.Field, unmarshalErr.Type.String()))

	case errors.As(err, &maxBytesErr):
		limitMB := float64(maxBytes) / (1024 * 1024)
		response.Error(w, r, http.StatusRequestEntityTooLarge, response.CodeBodyTooLarge,
			m.BodyTooLarge(limitMB))

	case errors.Is(err, io.EOF):
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			m.EmptyBody)

	case strings.HasPrefix(err.Error(), "json: unknown field"):
		field := strings.TrimPrefix(err.Error(), "json: unknown field ")
		response.Error(w, r, http.StatusBadRequest, response.CodeUnknownField,
			m.UnknownField(field))

	default:
		response.Error(w, r, http.StatusBadRequest, response.CodeInvalidJSON,
			m.InvalidBody)
	}
}
