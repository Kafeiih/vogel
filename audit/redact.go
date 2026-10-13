package audit

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
)

// RedactedValue replaces the value of every sensitive key before an entry
// reaches the Repository. The key itself is kept, so the audit trail still
// shows that the field was present (or changed) without storing its value.
const RedactedValue = "[REDACTED]"

// ErrUnredactable is wrapped by the error Recorder.Record returns, without
// persisting anything, when an entry's payload cannot be redacted: a map or
// slice that contains itself, nesting deeper than redaction descends, or a
// value encoding/json cannot encode (a channel, a func, a complex number, a
// NaN or infinite float). Redaction fails closed: an entry it cannot vouch
// for never reaches the Repository. A sensitive key's value is replaced
// without being inspected, so it never causes this error.
var ErrUnredactable = errors.New("audit: payload cannot be redacted")

// defaultRedactedKeys is the denylist every Recorder starts with. See
// WithRedactedKeys for the matching rules.
//
// "session" is deliberately left out: by substring it would redact
// session_count, session_duration and the session_id that correlates audit
// rows, which are routinely logged and rarely secret. Systems whose session
// identifiers are bearer secrets add it with WithRedactedKeys("session").
var defaultRedactedKeys = []string{
	"password", "passwd", "token", "secret", "authorization",
	"api_key", "private_key", "credential", "cookie",
}

// Redactor is a custom redaction hook installed with WithRedactor. It is
// called for every key at every nesting level of an entry's payloads whose
// key the denylist did not already match, with the value as the package
// stores it (map[string]any, []any, or a JSON scalar). Returning ok=false
// leaves the value to the remaining hooks and to the recursive walk.
//
// Returning ok=true replaces the value with replacement, and no later hook
// sees the value under that key. The replacement may be any value
// encoding/json can encode, including a struct, a typed map or slice, or the
// value the hook was given: it is normalized to its JSON form and walked like
// any other value, so a denylisted key nested inside it is still redacted and
// hooks still run on its nested keys. A replacement that cannot be redacted
// (one that contains itself, nests too deeply, or cannot be encoded) makes
// Record fail with ErrUnredactable. A scalar replacement such as a masked
// string is stored as is.
type Redactor func(key string, value any) (replacement any, ok bool)

// RecorderOption customizes a Recorder at construction time.
type RecorderOption func(*Recorder)

// WithRedactedKeys adds keys to the Recorder's redaction denylist, on top of
// the default one (password, passwd, token, secret, authorization, api_key,
// private_key, credential, cookie).
//
// Matching is case-insensitive and ignores '_', '-', '.' and spaces, and a
// denylist entry matches any key that contains it: "token" redacts
// "access_token", "refreshToken" and "X-Csrf-Token"; "api_key" redacts
// "apiKey" and "X-Api-Key"; "credential" redacts "credentials". Substring
// matching errs on the side of over-redaction ("token_count" is redacted too)
// because a leaked secret in an append-only audit log cannot be taken back,
// while an over-redacted counter can. Empty keys are ignored.
func WithRedactedKeys(keys ...string) RecorderOption {
	return func(r *Recorder) {
		r.redactedKeys = append(r.redactedKeys, keys...)
	}
}

// WithRedactor installs a custom redaction hook, for values the key denylist
// cannot express (masking an e-mail, dropping a field by value shape).
// Hooks run after the denylist, in the order they were added; the first one
// that returns ok=true wins.
func WithRedactor(fn Redactor) RecorderOption {
	return func(r *Recorder) {
		if fn != nil {
			r.redactors = append(r.redactors, fn)
		}
	}
}

// keyMatcher implements the denylist matching rules documented on
// WithRedactedKeys.
type keyMatcher struct {
	needles []string
}

func newKeyMatcher(keys []string) keyMatcher {
	m := keyMatcher{needles: make([]string, 0, len(keys))}
	for _, k := range keys {
		if n := normalizeKey(k); n != "" {
			m.needles = append(m.needles, n)
		}
	}
	return m
}

func (m keyMatcher) matches(key string) bool {
	k := normalizeKey(key)
	for _, n := range m.needles {
		if strings.Contains(k, n) {
			return true
		}
	}
	return false
}

func normalizeKey(key string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '_', '-', '.', ' ':
			return -1
		}
		return r
	}, strings.ToLower(key))
}

// maxRedactDepth bounds how deeply redaction descends into a payload. It
// matches the nesting depth after which encoding/json starts checking for
// cycles. The path-based cycle check catches a self-containing map or slice
// long before this; the cap is the backstop that keeps any payload from
// exhausting the stack.
const maxRedactDepth = 1000

// redactor walks an entry's payloads and returns redacted copies. It never
// writes to the maps and slices it is given.
type redactor struct {
	keys  keyMatcher
	hooks []Redactor
}

// walk is the state of one redaction pass over one payload: the maps and
// slices on the path from its root to the value being redacted, and the depth.
type walk struct {
	path  map[container]struct{}
	depth int
}

// container identifies a map or slice on the walk's path. A slice is
// identified by its data pointer and its length, the way encoding/json does:
// two slices of one backing array with different lengths (outer and
// outer[:1]) are different values, and neither contains the other merely by
// sharing memory. A genuine cycle repeats the same slice header, so it is
// still detected. A map has length -1 so it never collides with a slice.
type container struct {
	ptr uintptr
	len int
}

// redactEntry replaces e's payloads with redacted copies. When a payload
// cannot be redacted it returns an error wrapping ErrUnredactable and leaves
// e unchanged.
func (rd redactor) redactEntry(e *Entry) error {
	input, err := rd.redactMap(&walk{}, e.InputPayload)
	if err != nil {
		return fmt.Errorf("input payload: %w", err)
	}
	before, err := rd.redactMap(&walk{}, e.BeforeSnapshot)
	if err != nil {
		return fmt.Errorf("before snapshot: %w", err)
	}
	after, err := rd.redactMap(&walk{}, e.AfterSnapshot)
	if err != nil {
		return fmt.Errorf("after snapshot: %w", err)
	}
	changes, err := rd.redactChanges(&walk{}, e.ChangedFields)
	if err != nil {
		return fmt.Errorf("changed fields: %w", err)
	}
	e.InputPayload, e.BeforeSnapshot, e.AfterSnapshot, e.ChangedFields = input, before, after, changes
	return nil
}

// redactChanges redacts ChangedFields. An entry in the {"old", "new"} shape
// that WithSubject and WithChange produce is redacted side by side under its
// field name, so a sensitive field still shows up as changed (and whether a
// side was nil) without either value.
func (rd redactor) redactChanges(w *walk, changes map[string]any) (map[string]any, error) {
	if changes == nil {
		return nil, nil
	}
	out := make(map[string]any, len(changes))
	for k, v := range changes {
		if diff, ok := v.(map[string]any); ok && isDiff(diff) {
			sides := make(map[string]any, len(diff))
			for side, sv := range diff {
				rv, err := rd.redactField(w, k, sv)
				if err != nil {
					return nil, err
				}
				sides[side] = rv
			}
			out[k] = sides
			continue
		}
		rv, err := rd.redactField(w, k, v)
		if err != nil {
			return nil, err
		}
		out[k] = rv
	}
	return out, nil
}

func isDiff(m map[string]any) bool {
	if len(m) == 0 {
		return false
	}
	for k := range m {
		if k != "old" && k != "new" {
			return false
		}
	}
	return true
}

func (rd redactor) redactMap(w *walk, m map[string]any) (map[string]any, error) {
	if m == nil {
		return nil, nil
	}
	leave, err := w.enter(container{ptr: reflect.ValueOf(m).Pointer(), len: -1})
	if err != nil {
		return nil, err
	}
	defer leave()

	out := make(map[string]any, len(m))
	for k, v := range m {
		rv, err := rd.redactField(w, k, v)
		if err != nil {
			return nil, err
		}
		out[k] = rv
	}
	return out, nil
}

// redactField redacts value as found under key. A nil value is kept even
// under a sensitive key: it carries no secret, and it records whether the
// field was set at all. A sensitive key's value is replaced without being
// inspected, so it is redacted even when it could not be encoded.
func (rd redactor) redactField(w *walk, key string, value any) (any, error) {
	if rd.keys.matches(key) {
		if value == nil {
			return nil, nil
		}
		return RedactedValue, nil
	}
	value, err := normalizeValue(value)
	if err != nil {
		return nil, fmt.Errorf("key %q: %w", key, err)
	}
	for _, h := range rd.hooks {
		if repl, ok := h(key, value); ok {
			// The replacement goes through the same walk as the value it
			// replaces, so a hook cannot reintroduce a denylisted key. The
			// walk descends into the replacement and never calls hooks again
			// for this key, so a hook returning its input cannot recurse.
			if value, err = normalizeValue(repl); err != nil {
				return nil, fmt.Errorf("key %q: redactor replacement: %w", key, err)
			}
			break
		}
	}
	return rd.redactValue(w, value)
}

// redactValue redacts a value normalizeValue already returned.
func (rd redactor) redactValue(w *walk, value any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		return rd.redactMap(w, v)
	case []any:
		if v == nil {
			return nil, nil
		}
		leave, err := w.enter(container{ptr: reflect.ValueOf(v).Pointer(), len: len(v)})
		if err != nil {
			return nil, err
		}
		defer leave()

		out := make([]any, len(v))
		for i, item := range v {
			item, err := normalizeValue(item)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			if out[i], err = rd.redactValue(w, item); err != nil {
				return nil, err
			}
		}
		return out, nil
	default:
		return v, nil
	}
}

// enter records that the walk descends into the map or slice c, and returns
// the function that undoes it on the way back up. Only the current path is
// tracked, so a value shared by two siblings is not mistaken for a cycle. An
// empty slice cannot contain anything, itself included, so it only counts
// towards the depth; that also keeps empty slices that share a data pointer
// (the runtime's zero-size allocation) from looking like a cycle. Past
// maxRedactDepth the walk fails closed with ErrUnredactable.
func (w *walk) enter(c container) (leave func(), err error) {
	if w.depth >= maxRedactDepth {
		return nil, fmt.Errorf("%w: nested deeper than %d levels", ErrUnredactable, maxRedactDepth)
	}
	tracked := c.ptr != 0 && c.len != 0
	if tracked {
		if _, onPath := w.path[c]; onPath {
			return nil, fmt.Errorf("%w: value contains itself", ErrUnredactable)
		}
		if w.path == nil {
			w.path = make(map[container]struct{})
		}
		w.path[c] = struct{}{}
	}
	w.depth++
	return func() {
		w.depth--
		if tracked {
			delete(w.path, c)
		}
	}, nil
}

// normalizeValue converts a value the package did not build itself (a struct,
// a pointer, a typed map or slice, a type with its own JSON or text encoding)
// into the generic form encoding/json gives it, because that JSON form is what
// the Repository persists — redacting it is what keeps a struct's "password"
// field out of the log. Numbers decode as json.Number so they re-encode
// unchanged.
//
// A value that cannot be encoded (a channel, a func, a complex number, a NaN
// or infinite float, a cyclic pointer graph) is an error wrapping
// ErrUnredactable: passing it through unredacted would let whatever it holds
// bypass redaction, so Record refuses the entry instead.
func normalizeValue(value any) (any, error) {
	switch value.(type) {
	case nil, map[string]any, []any, string, bool, json.Number:
		return value, nil
	}
	if !needsJSONForm(value) {
		return value, nil
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnredactable, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnredactable, err)
	}
	return out, nil
}

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// needsJSONForm reports whether value must go through encoding/json before it
// can be redacted or stored. Plain booleans, integers, finite floats and
// strings are kept as they are. Everything else, including any type with its
// own JSON or text encoding, is normalized, so that the encoder rejects what
// it cannot encode instead of the value slipping through unredacted.
func needsJSONForm(value any) bool {
	t := reflect.TypeOf(value)
	if t.Implements(jsonMarshalerType) || t.Implements(textMarshalerType) {
		return true
	}
	switch t.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return false
	case reflect.Float32, reflect.Float64:
		f := reflect.ValueOf(value).Float()
		return math.IsNaN(f) || math.IsInf(f, 0)
	default:
		return true
	}
}
