package validation

import (
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/request"
)

// Validator wraps a go-playground/validator instance configured to name its
// fields after their JSON tags, plus the Messages used to translate a
// failing tag into a client-facing string.
type Validator struct {
	validate *validator.Validate
	messages Messages
}

// New builds a Validator using validator.WithRequiredStructEnabled (so a
// struct field itself can carry "required", not only its members) and
// jsonTagName (so every reported field name is the JSON key a client
// actually sent, not the Go struct field name).
func New(opts ...Option) *Validator {
	vd := validator.New(validator.WithRequiredStructEnabled())
	vd.RegisterTagNameFunc(jsonTagName)

	v := &Validator{validate: vd, messages: DefaultMessages()}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// jsonTagName is validator.RegisterTagNameFunc: it names every field after
// its json tag (the part before the first comma, so "name,omitempty"
// becomes "name"), falling back to the Go field name when there is no json
// tag, the tag is empty, or the field is explicitly excluded (json:"-") —
// exactly the three cases where encoding/json itself would not have a JSON
// name to report either.
func jsonTagName(fld reflect.StructField) string {
	tag := fld.Tag.Get("json")
	name, _, _ := strings.Cut(tag, ",")
	if name == "" || name == "-" {
		return fld.Name
	}
	return name
}

// Struct validates v and reports (nil, nil) when it is valid.
//
// On a validation failure (validator.ValidationErrors), it returns one
// FieldErrors entry per failing field, keyed by v's Namespace() with the
// root struct's own name stripped — the JSON path leading to the field
// (nested structs and dive'd slices/maps included, e.g. "address.street" or
// "items[0].monto") — and valued with a message drawn from v's Messages.
//
// Any other error — chiefly *validator.InvalidValidationError, returned when
// v is not a struct, is nil, or is a nil pointer — is a programming error,
// not a client validation failure, and is returned as-is. Never send its
// Error() text to a client: it names Go types, not request fields.
func (v *Validator) Struct(val any) (request.FieldErrors, error) {
	err := v.validate.Struct(val)
	if err == nil {
		return nil, nil
	}

	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		return nil, err
	}

	fields := make(request.FieldErrors, len(verrs))
	for _, fe := range verrs {
		key := fieldKey(fe)
		if _, exists := fields[key]; exists {
			// go-playground/validator short-circuits a field's tag chain at
			// its first failing tag, so it never reports two errors for the
			// same field in practice (verified against v10.30.1). This
			// branch is defensive: if that ever changed, the first failing
			// tag's message — the one a caller would fix first — is the one
			// kept, rather than an arbitrary later one silently overwriting
			// it.
			continue
		}
		fields[key] = v.messageFor(fe)
	}
	return fields, nil
}

// fieldKey turns fe.Namespace() — which always starts with the root
// struct's own name (e.g. "CreateInvoiceRequest.address.street") — into the
// JSON path a client can act on ("address.street"), by stripping everything
// up to and including the first '.'. A namespace with no '.' at all (a
// non-nested field on a struct with no name segment, which validator does
// not produce for Struct()) is returned unchanged rather than emptied.
func fieldKey(fe validator.FieldError) string {
	ns := fe.Namespace()
	if i := strings.IndexByte(ns, '.'); i >= 0 {
		return ns[i+1:]
	}
	return ns
}

// messageFor renders fe using v's Messages, dispatching on the failing
// tag. A tag with no dedicated Messages field falls back to Messages.Default.
func (v *Validator) messageFor(fe validator.FieldError) string {
	field := fieldKey(fe)
	switch fe.Tag() {
	case "required":
		return v.messages.Required(field)
	case "min":
		return v.messages.Min(field, fe.Param(), fe.Kind())
	case "max":
		return v.messages.Max(field, fe.Param(), fe.Kind())
	case "oneof":
		return v.messages.OneOf(field, fe.Param())
	default:
		return v.messages.Default(field, fe.Tag())
	}
}

// Write validates v and reports true when it is valid, writing nothing to
// w.
//
// On a validation failure it writes a response.ValidationError with the
// per-field messages Struct produced and returns false. On a programming
// error (see Struct) it writes a generic 500 via response.Error using
// response.CodeInternalError, never the underlying Go error text, and
// returns false. Write has no logger dependency — it cannot log the
// underlying error itself — so a caller that wants the error logged should
// call Struct directly instead:
//
//	fields, err := v.Struct(&in)
//	if err != nil {
//	    logger.Error("validating request", "error", err)
//	    response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to validate request")
//	    return
//	}
//	if fields != nil {
//	    response.ValidationError(w, r, fields)
//	    return
//	}
func (v *Validator) Write(w http.ResponseWriter, r *http.Request, val any) bool {
	fields, err := v.Struct(val)
	if err != nil {
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to validate request")
		return false
	}
	if fields == nil {
		return true
	}
	response.ValidationError(w, r, fields)
	return false
}

// defaultValidator is the instance the package-level Struct and Write use,
// built with DefaultMessages, exactly like request's package-level
// functions use a default Decoder.
var defaultValidator = New()

// Struct validates v using defaultValidator (DefaultMessages). See
// (*Validator).Struct.
func Struct(v any) (request.FieldErrors, error) {
	return defaultValidator.Struct(v)
}

// Write validates v using defaultValidator (DefaultMessages) and writes the
// response. See (*Validator).Write.
func Write(w http.ResponseWriter, r *http.Request, v any) bool {
	return defaultValidator.Write(w, r, v)
}
