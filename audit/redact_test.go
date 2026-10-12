package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordOne records a single entry through a Recorder built with opts and
// returns what reached the repository.
func recordOne(t *testing.T, ropts []RecorderOption, opts ...Option) *Entry {
	t.Helper()
	repo := &fakeRepository{}
	r := NewRecorder(repo, ropts...)

	err := r.Record(context.Background(), nil, SourceHTTP, ActionUpdate, "widget", "w-1", opts...)
	require.NoError(t, err)
	require.Len(t, repo.created, 1)
	return repo.created[0]
}

func TestSensitiveKey_DefaultDenylistMatching(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"password", true},
		{"Password", true},
		{"new_password", true},
		{"passwordHash", true},
		{"token", true},
		{"access_token", true},
		{"refreshToken", true},
		{"secret", true},
		{"client_secret", true},
		{"Authorization", true},
		{"api_key", true},
		{"apiKey", true},
		{"X-Api-Key", true},
		{"cookie", true},
		{"Set-Cookie", true},
		{"username", false},
		{"email", false},
		{"name", false},
		{"api", false},
		{"key", false},
	}

	m := newKeyMatcher(defaultRedactedKeys)
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			assert.Equal(t, tt.want, m.matches(tt.key))
		})
	}
}

func TestRecord_RedactsSensitiveInputPayloadKeys(t *testing.T) {
	payload := map[string]any{
		"username":      "alice",
		"password":      "hunter2",
		"Authorization": "Bearer abc",
		"refresh_token": "rt-1",
		"age":           42,
	}

	e := recordOne(t, nil, WithInputPayload(payload))

	assert.Equal(t, map[string]any{
		"username":      "alice",
		"password":      RedactedValue,
		"Authorization": RedactedValue,
		"refresh_token": RedactedValue,
		"age":           42,
	}, e.InputPayload)
}

func TestRecord_RedactsNestedMapsAndSlices(t *testing.T) {
	payload := map[string]any{
		"user": map[string]any{
			"name":        "alice",
			"credentials": map[string]any{"api_key": "k-1", "scope": "read"},
		},
		"sessions": []any{
			map[string]any{"id": "s1", "cookie": "c1"},
			"plain",
		},
		"api_key": map[string]any{"value": "whole-subtree-goes"},
	}

	e := recordOne(t, nil, WithInputPayload(payload))

	assert.Equal(t, map[string]any{
		"user": map[string]any{
			"name":        "alice",
			"credentials": map[string]any{"api_key": RedactedValue, "scope": "read"},
		},
		"sessions": []any{
			map[string]any{"id": "s1", "cookie": RedactedValue},
			"plain",
		},
		"api_key": RedactedValue,
	}, e.InputPayload)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Values the package does not build itself (structs, typed maps and slices)
// are persisted through encoding/json, so they are redacted in their JSON
// form: a struct's sensitive fields cannot slip through by not being a
// map[string]any.
func TestRecord_RedactsStructAndTypedValuesByJSONForm(t *testing.T) {
	payload := map[string]any{
		"body":    loginRequest{Email: "a@b.c", Password: "hunter2"},
		"ptr":     &loginRequest{Email: "x@y.z", Password: "p"},
		"headers": map[string]string{"Cookie": "sid=1", "Accept": "json"},
		"list":    []loginRequest{{Email: "l@m.n", Password: "q"}},
	}

	e := recordOne(t, nil, WithInputPayload(payload))

	assert.Equal(t, map[string]any{
		"body":    map[string]any{"email": "a@b.c", "password": RedactedValue},
		"ptr":     map[string]any{"email": "x@y.z", "password": RedactedValue},
		"headers": map[string]any{"Cookie": RedactedValue, "Accept": "json"},
		"list":    []any{map[string]any{"email": "l@m.n", "password": RedactedValue}},
	}, e.InputPayload)
}

func TestRecord_RedactsSnapshotsAndKeepsChangedFieldNames(t *testing.T) {
	before := fakeAuditable{repr: "u", snapshot: map[string]any{"name": "a", "password": "old-pw"}}
	after := fakeAuditable{repr: "u", snapshot: map[string]any{"name": "b", "password": "new-pw"}}

	e := recordOne(t, nil, WithChange(before, after))

	assert.Equal(t, map[string]any{"name": "a", "password": RedactedValue}, e.BeforeSnapshot)
	assert.Equal(t, map[string]any{"name": "b", "password": RedactedValue}, e.AfterSnapshot)
	assert.Equal(t, map[string]any{
		"name":     map[string]any{"old": "a", "new": "b"},
		"password": map[string]any{"old": RedactedValue, "new": RedactedValue},
	}, e.ChangedFields)
}

// A nil value carries no secret, and keeping it preserves whether a sensitive
// field was set or cleared — the one thing an auditor still needs to know.
func TestRecord_ChangedFields_KeepsNilSideOfSensitiveDiff(t *testing.T) {
	subject := fakeAuditable{repr: "u", snapshot: map[string]any{"api_key": "k"}}

	repo := &fakeRepository{}
	err := NewRecorder(repo).Record(context.Background(), nil, SourceHTTP, ActionCreate, "user", "u-1", WithSubject(subject))
	require.NoError(t, err)

	e := repo.created[0]
	assert.Equal(t, map[string]any{"api_key": RedactedValue}, e.AfterSnapshot)
	assert.Equal(t, map[string]any{
		"api_key": map[string]any{"old": nil, "new": RedactedValue},
	}, e.ChangedFields)
}

func TestRecord_DoesNotMutateCallerInput(t *testing.T) {
	nested := map[string]any{"token": "t-1"}
	list := []any{map[string]any{"secret": "s-1"}}
	payload := map[string]any{"password": "pw", "nested": nested, "list": list}
	changed := map[string]any{"password": map[string]any{"old": "a", "new": "b"}}

	e := recordOne(t, nil, WithInputPayload(payload), WithChangedFields(changed))

	assert.Equal(t, RedactedValue, e.InputPayload["password"])
	assert.Equal(t, "pw", payload["password"])
	assert.Equal(t, "t-1", nested["token"])
	assert.Equal(t, "s-1", list[0].(map[string]any)["secret"])
	assert.Equal(t, map[string]any{"old": "a", "new": "b"}, changed["password"])
}

func TestWithRedactedKeys_ExtendsDefaultDenylist(t *testing.T) {
	payload := map[string]any{"SSN": "123", "national_ssn_hash": "h", "password": "pw", "name": "a"}

	e := recordOne(t, []RecorderOption{WithRedactedKeys("ssn")}, WithInputPayload(payload))

	assert.Equal(t, map[string]any{
		"SSN":               RedactedValue,
		"national_ssn_hash": RedactedValue,
		"password":          RedactedValue,
		"name":              "a",
	}, e.InputPayload)
}

func TestWithRedactor_CustomHookMasksValues(t *testing.T) {
	maskEmail := func(key string, value any) (any, bool) {
		s, ok := value.(string)
		if !ok || !strings.EqualFold(key, "email") {
			return nil, false
		}
		at := strings.IndexByte(s, '@')
		if at < 0 {
			return RedactedValue, true
		}
		return "***" + s[at:], true
	}
	payload := map[string]any{
		"email":    "alice@example.com",
		"password": "pw",
		"contact":  map[string]any{"email": "bob@example.com"},
	}

	e := recordOne(t, []RecorderOption{WithRedactor(maskEmail)}, WithInputPayload(payload))

	assert.Equal(t, map[string]any{
		"email":    "***@example.com",
		"password": RedactedValue,
		"contact":  map[string]any{"email": "***@example.com"},
	}, e.InputPayload)
}

func TestRecord_NilPayloadsStayNil(t *testing.T) {
	e := recordOne(t, nil)

	assert.Nil(t, e.InputPayload)
	assert.Nil(t, e.BeforeSnapshot)
	assert.Nil(t, e.AfterSnapshot)
	assert.Nil(t, e.ChangedFields)
}
