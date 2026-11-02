// Package idempotency makes a mutation safe to retry: a caller sends a key
// with the request, and a retry with the same key gets the first attempt's
// response back instead of running the mutation again.
//
// It comes in two layers that share one store (idempotency/postgres) and one
// table (idempotency/migrations):
//
//   - Store.Do runs inside the mutation's own transaction. The key, the
//     mutation and its stored response commit or roll back together, so the
//     mutation runs exactly once per key. Services adopt it explicitly.
//   - The HTTP middleware (idempotency/httpx) wraps any handler. It reserves
//     the key with a lease, runs the handler and stores the response in
//     separate statements: if the process dies after the handler committed
//     but before the response was stored, a retry after the lease runs the
//     handler again. Routes that cannot afford that use Store.Do.
//
// A key is scoped (Request.Scope): the same key from another caller, or for
// another operation, is a different key. A key reused with different request
// content (Request.Fingerprint) is rejected with ErrKeyReused rather than
// answered with the response of another request.
package idempotency

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// MaxKeyLength bounds Request.Key. Keys are opaque client strings (a UUID is
// the usual choice); the bound keeps a hostile client from storing large
// values in the key column.
const MaxKeyLength = 255

var (
	// ErrInvalidRequest reports a Request the store cannot claim: an empty
	// scope, an empty key or one longer than MaxKeyLength, a key with a
	// character outside printable ASCII, or an empty fingerprint.
	ErrInvalidRequest = errors.New("idempotency: invalid request")
	// ErrKeyReused reports a key already claimed, in the same scope, by a
	// request with another fingerprint.
	ErrKeyReused = errors.New("idempotency: key reused with a different request")
	// ErrInProgress reports a key whose first request is still running.
	ErrInProgress = errors.New("idempotency: request with this key is still in progress")
)

// Request identifies one idempotent operation.
type Request struct {
	// Scope names whose operation it is and which one, so keys never collide
	// across callers or operations (the HTTP middleware uses the principal,
	// the method and the route).
	Scope string
	// Key is the client's idempotency key.
	Key string
	// Fingerprint identifies the request content (see Fingerprint): a retry
	// must send the same content with the same key.
	Fingerprint []byte
}

// Validate reports whether r can be claimed, wrapping ErrInvalidRequest.
func (r Request) Validate() error {
	switch {
	case r.Scope == "":
		return fmt.Errorf("%w: empty scope", ErrInvalidRequest)
	case r.Key == "":
		return fmt.Errorf("%w: empty key", ErrInvalidRequest)
	case len(r.Key) > MaxKeyLength:
		return fmt.Errorf("%w: key longer than %d bytes", ErrInvalidRequest, MaxKeyLength)
	case len(r.Fingerprint) == 0:
		return fmt.Errorf("%w: empty fingerprint", ErrInvalidRequest)
	}
	for i := 0; i < len(r.Key); i++ {
		if c := r.Key[i]; c < 0x20 || c > 0x7e {
			return fmt.Errorf("%w: key has a character outside printable ASCII", ErrInvalidRequest)
		}
	}
	return nil
}

// Response is what an idempotent operation produced: stored once and
// returned to every retry. Store.Do callers outside HTTP may leave Status
// zero and Header nil and carry their result in Body.
type Response struct {
	Status int
	Header map[string][]string
	Body   []byte
}

// Reservation is the outcome of reserving a key outside a transaction (the
// HTTP middleware's path; see idempotency/postgres.Store.Reserve). Exactly
// one field is set: Token when this request now holds the key and must run
// its operation, then complete or release the reservation with that token;
// Replay when the key's operation already finished, holding its stored
// response.
type Reservation struct {
	Token  uuid.UUID
	Replay *Response
}

// Fingerprint returns the SHA-256 of parts, each length-prefixed so that
// ("ab", "c") and ("a", "bc") never collide.
func Fingerprint(parts ...[]byte) []byte {
	h := sha256.New()
	var n [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write(p)
	}
	return h.Sum(nil)
}
