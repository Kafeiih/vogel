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
// FieldErrors entry per failing field, keyed by the JSON path leading to the
// field (nested structs and dive'd slices/maps included, e.g.
// "address.street" or "items[0].monto") — see fieldKey for exactly how that
// path is derived, including how an embedded (anonymous) struct field
// flattens into its parent exactly as encoding/json would — and valued with
// a message drawn from v's Messages.
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

	root := derefStruct(reflect.TypeOf(val))
	fields := make(request.FieldErrors, len(verrs))
	for _, fe := range verrs {
		key := fieldKey(root, fe)
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
		fields[key] = v.messageFor(key, fe)
	}
	return fields, nil
}

// fieldKey turns fe.Namespace() and fe.StructNamespace() into the JSON path
// a client can act on ("address.street"), stripping the root struct's own
// leading segment, and — unlike a plain Namespace() strip — flattening an
// embedded (anonymous) struct field exactly the way encoding/json would:
//
//   - an anonymous field with NO json tag name contributes no segment of its
//     own; its own fields are promoted one level up (e.g. Base.id -> id).
//   - an anonymous field WITH an explicit json tag name is NOT flattened and
//     keeps that name as an ordinary segment (e.g. Base json:"base" -> base.id).
//
// This cannot be recovered from fe.Namespace() alone: RegisterTagNameFunc
// (see jsonTagName) already substituted every segment's JSON name, including
// an anonymous field's own name when it has none of its own — falling back
// to the Go field name exactly like it does for any other untagged field —
// so an untagged embedded Base surfaces there indistinguishably from an
// ordinary field named Base. fe.StructNamespace() gives the same path shape
// using unsubstituted Go field names instead, which — walked one segment at
// a time against root's reflect.Type — tells us exactly which segments are
// anonymous-and-untagged and must be dropped. The two namespaces always
// share the same segment count and the same "[i]"/"[key]" index suffixes,
// since validator builds both from the same struct walk.
//
// root is the reflect.Type Struct validated (already deref'd through any
// pointer). Any segment this can no longer type-walk (a field FieldByName
// can't find, or a non-struct type such as a map value or interface) is
// passed through unflattened rather than guessed at or panicking.
func fieldKey(root reflect.Type, fe validator.FieldError) string {
	jsonSegs := splitNamespace(fe.Namespace())
	goSegs := splitNamespace(fe.StructNamespace())
	if len(jsonSegs) > 0 {
		jsonSegs = jsonSegs[1:]
	}
	if len(goSegs) > 0 {
		goSegs = goSegs[1:]
	}

	t := root
	out := make([]string, 0, len(jsonSegs))
	for i, goSeg := range goSegs {
		name, hasIndex := cutIndex(goSeg)

		flatten := false
		if t != nil && t.Kind() == reflect.Struct {
			if fld, ok := t.FieldByName(name); ok {
				flatten = fld.Anonymous && jsonNameOf(fld) == ""
				next := fld.Type
				if hasIndex {
					next = elemOf(next)
				}
				t = derefStruct(next)
			} else {
				t = nil
			}
		} else {
			t = nil
		}

		if flatten {
			continue
		}
		if i < len(jsonSegs) {
			out = append(out, jsonSegs[i])
		}
	}
	return strings.Join(out, ".")
}

// splitNamespace splits a validator namespace on '.'; the "[i]"/"[key]" dive
// suffix validator attaches never itself contains a dot, so a plain split is
// exact. An empty namespace splits to nil, not [""].
func splitNamespace(ns string) []string {
	if ns == "" {
		return nil
	}
	return strings.Split(ns, ".")
}

// cutIndex splits a namespace segment such as "Items[0]" into its field name
// ("Items") and whether a dive index was present. A segment with no index
// (the common case) is returned unchanged.
func cutIndex(seg string) (name string, hasIndex bool) {
	if i := strings.IndexByte(seg, '['); i >= 0 {
		return seg[:i], true
	}
	return seg, false
}

// derefStruct dereferences t through any number of pointers, returning
// whatever type sits at the bottom (a struct, in the common case, but
// possibly something else — the caller re-checks Kind()). A nil t stays nil.
func derefStruct(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// elemOf dereferences t and, if it names a slice, array or map (the three
// kinds "dive" walks), returns its element type; any other kind is returned
// unchanged, since the only caller (fieldKey) only calls this on a field
// whose namespace segment carried a dive index.
func elemOf(t reflect.Type) reflect.Type {
	t = derefStruct(t)
	if t == nil {
		return nil
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return t.Elem()
	default:
		return t
	}
}

// jsonNameOf returns fld's explicit json tag name (the part before the
// first comma), or "" when it has none of its own — no tag, an empty tag, or
// json:"-" — mirroring encoding/json's own criterion for whether an
// anonymous field is flattened into its parent (see fieldKey).
func jsonNameOf(fld reflect.StructField) string {
	tag := fld.Tag.Get("json")
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return ""
	}
	return name
}

// messageFor renders fe using v's Messages, dispatching on the failing tag.
// field is fe's already-resolved client-facing key (see fieldKey) — passed
// in rather than recomputed here so Struct only ever walks fe's namespace
// once. A tag with no dedicated Messages field falls back to Messages.Default.
func (v *Validator) messageFor(field string, fe validator.FieldError) string {
	switch fe.Tag() {
	case "required",
		"required_if", "required_unless",
		"required_with", "required_with_all",
		"required_without", "required_without_all":
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
