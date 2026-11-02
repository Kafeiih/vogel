package idempotency_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/idempotency"
)

func TestRequest_Validate(t *testing.T) {
	ok := idempotency.Request{Scope: "user-1 POST /cases", Key: "0b6f6c8e-6d1e-4a8b-9c1d-2f0f8a3b9e11", Fingerprint: []byte{1}}
	require.NoError(t, ok.Validate())

	for name, mutate := range map[string]func(*idempotency.Request){
		"empty scope":       func(r *idempotency.Request) { r.Scope = "" },
		"empty key":         func(r *idempotency.Request) { r.Key = "" },
		"key too long":      func(r *idempotency.Request) { r.Key = strings.Repeat("k", idempotency.MaxKeyLength+1) },
		"control character": func(r *idempotency.Request) { r.Key = "a\nb" },
		"non-ASCII":         func(r *idempotency.Request) { r.Key = "clave-ñ" },
		"empty fingerprint": func(r *idempotency.Request) { r.Fingerprint = nil },
	} {
		t.Run(name, func(t *testing.T) {
			r := ok
			mutate(&r)
			assert.ErrorIs(t, r.Validate(), idempotency.ErrInvalidRequest)
		})
	}

	longest := ok
	longest.Key = strings.Repeat("k", idempotency.MaxKeyLength)
	assert.NoError(t, longest.Validate(), "a key of exactly MaxKeyLength bytes is valid")
}

func TestFingerprint_LengthPrefixesParts(t *testing.T) {
	a := idempotency.Fingerprint([]byte("ab"), []byte("c"))
	b := idempotency.Fingerprint([]byte("a"), []byte("bc"))
	assert.NotEqual(t, a, b, "moving a byte across parts changes the fingerprint")
	assert.Len(t, a, 32)
	assert.Equal(t, a, idempotency.Fingerprint([]byte("ab"), []byte("c")), "the fingerprint is deterministic")
}
