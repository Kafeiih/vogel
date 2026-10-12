package audit

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
)

// RedactedValue replaces the value of every sensitive key before an entry
// reaches the Repository. The key itself is kept, so the audit trail still
// shows that the field was present (or changed) without storing its value.
const RedactedValue = "[REDACTED]"

// defaultRedactedKeys is the denylist every Recorder starts with. See
// WithRedactedKeys for the matching rules.
var defaultRedactedKeys = []string{"password", "token", "secret", "authorization", "api_key", "cookie"}

// Redactor is a custom redaction hook installed with WithRedactor. It is
// called for every key at every nesting level of an entry's payloads whose
// key the denylist did not already match, with the value as the package
// stores it (map[string]any, []any, or a JSON scalar). Returning ok=true
// replaces the value with replacement and stops descending into it;
// returning ok=false leaves the value to the remaining hooks and to the
// recursive walk.
type Redactor func(key string, value any) (replacement any, ok bool)

// RecorderOption customizes a Recorder at construction time.
type RecorderOption func(*Recorder)

// WithRedactedKeys adds keys to the Recorder's redaction denylist, on top of
// the default one (password, token, secret, authorization, api_key, cookie).
//
// Matching is case-insensitive and ignores '_', '-', '.' and spaces, and a
// denylist entry matches any key that contains it: "token" redacts
// "access_token", "refreshToken" and "X-Csrf-Token"; "api_key" redacts
// "apiKey" and "X-Api-Key". Substring matching errs on the side of
// over-redaction ("token_count" is redacted too) because a leaked secret in
// an append-only audit log cannot be taken back, while an over-redacted
// counter can. Empty keys are ignored.
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

// redactor walks an entry's payloads and returns redacted copies. It never
// writes to the maps and slices it is given.
type redactor struct {
	keys  keyMatcher
	hooks []Redactor
}

// redactEntry replaces e's payloads with redacted copies.
func (rd redactor) redactEntry(e *Entry) {
	e.InputPayload = rd.redactMap(e.InputPayload)
	e.BeforeSnapshot = rd.redactMap(e.BeforeSnapshot)
	e.AfterSnapshot = rd.redactMap(e.AfterSnapshot)
	e.ChangedFields = rd.redactChanges(e.ChangedFields)
}

// redactChanges redacts ChangedFields. An entry in the {"old", "new"} shape
// that WithSubject and WithChange produce is redacted side by side under its
// field name, so a sensitive field still shows up as changed (and whether a
// side was nil) without either value.
func (rd redactor) redactChanges(changes map[string]any) map[string]any {
	if changes == nil {
		return nil
	}
	out := make(map[string]any, len(changes))
	for k, v := range changes {
		if diff, ok := v.(map[string]any); ok && isDiff(diff) {
			sides := make(map[string]any, len(diff))
			for side, sv := range diff {
				sides[side] = rd.redactField(k, sv)
			}
			out[k] = sides
			continue
		}
		out[k] = rd.redactField(k, v)
	}
	return out
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

func (rd redactor) redactMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = rd.redactField(k, v)
	}
	return out
}

// redactField redacts value as found under key. A nil value is kept even
// under a sensitive key: it carries no secret, and it records whether the
// field was set at all.
func (rd redactor) redactField(key string, value any) any {
	if rd.keys.matches(key) {
		if value == nil {
			return nil
		}
		return RedactedValue
	}
	value = normalizeValue(value)
	for _, h := range rd.hooks {
		if repl, ok := h(key, value); ok {
			return repl
		}
	}
	return rd.redactValue(value)
}

func (rd redactor) redactValue(value any) any {
	switch v := normalizeValue(value).(type) {
	case map[string]any:
		return rd.redactMap(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = rd.redactValue(item)
		}
		return out
	default:
		return v
	}
}

// normalizeValue converts a composite value the package did not build itself
// (a struct, a pointer, a typed map or slice) into the generic form
// encoding/json gives it, because that JSON form is what the Repository
// persists — redacting it is what keeps a struct's "password" field out of
// the log. Numbers decode as json.Number so they re-encode unchanged. A value
// that cannot be encoded is returned as is: the Repository fails on it too,
// so nothing is persisted.
func normalizeValue(value any) any {
	switch value.(type) {
	case nil, map[string]any, []any, string, bool, json.Number:
		return value
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array, reflect.Pointer, reflect.Interface:
	default:
		return value
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return value
	}
	return out
}
