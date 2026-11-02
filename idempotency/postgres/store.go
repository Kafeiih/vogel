// Package postgres is the PostgreSQL store of vogel/idempotency, on the
// idempotency_key table of vogel/idempotency/migrations.
package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kafeiih/vogel/idempotency"
	"github.com/kafeiih/vogel/pgxtx"
)

// DefaultTTL is how long a key protects against a retry unless WithTTL sets
// another duration.
const DefaultTTL = 24 * time.Hour

// defaultDeleteLimit is the batch size of DeleteExpired for a limit <= 0.
const defaultDeleteLimit = 1000

// Store claims idempotency keys and stores the responses of the operations
// they protect.
type Store struct {
	ttl time.Duration
}

// Option configures a Store.
type Option func(*Store)

// WithTTL sets how long a claimed key protects against a retry. A d <= 0
// keeps DefaultTTL.
func WithTTL(d time.Duration) Option {
	return func(s *Store) {
		if d > 0 {
			s.ttl = d
		}
	}
}

// NewStore creates a Store.
func NewStore(opts ...Option) *Store {
	s := &Store{ttl: DefaultTTL}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// claimSQL inserts the key as in progress, or reclaims an expired row. It
// returns a row only when this call owns the key. On a conflict with an
// uncommitted insert of the same key, Postgres makes it wait until that
// transaction ends: a commit leaves the row in place (no row returned), a
// rollback lets this insert go ahead. The DO UPDATE locks the existing row
// even when its WHERE is false, so the caller's transaction reads it stably.
const claimSQL = `
    INSERT INTO idempotency_key (scope, key, fingerprint, status, expires_at)
    VALUES ($1, $2, $3, 'in_progress', now() + $4 * interval '1 microsecond')
    ON CONFLICT (scope, key) DO UPDATE
       SET fingerprint = EXCLUDED.fingerprint, status = 'in_progress',
           response_status = NULL, response_header = NULL, response_body = NULL,
           created_at = now(), locked_until = NULL, reservation = NULL, expires_at = EXCLUDED.expires_at
     WHERE idempotency_key.expires_at <= now()
    RETURNING true`

// Do runs fn exactly once per key, inside tx.
//
// It claims (req.Scope, req.Key) in tx. When the key is new, or its earlier
// claim expired, it runs fn — which must do its writes on the same tx — and
// stores fn's Response in tx too, so the key, the mutation and the response
// commit or roll back together: an error from fn, or a rollback by the
// caller, leaves no trace of the key. It then returns fn's Response with
// replayed false; an error from fn is returned as is.
//
// When the key is already claimed by a committed transaction, Do does not
// call fn. It returns the stored Response with replayed true when the
// fingerprint matches and the response is stored, ErrKeyReused when the
// fingerprint differs, and ErrInProgress when the key is held by an
// in-progress reservation of the HTTP middleware. A duplicate running while
// the first transaction is still open waits for it, then replays (commit) or
// runs fn (rollback).
//
// An invalid req fails with ErrInvalidRequest before touching tx.
func (s *Store) Do(ctx context.Context, tx pgx.Tx, req idempotency.Request, fn func(context.Context) (idempotency.Response, error)) (idempotency.Response, bool, error) {
	if err := req.Validate(); err != nil {
		return idempotency.Response{}, false, err
	}
	var owned bool
	err := tx.QueryRow(ctx, claimSQL, req.Scope, req.Key, req.Fingerprint, s.ttl.Microseconds()).Scan(&owned)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return s.existing(ctx, tx, req)
	case err != nil:
		return idempotency.Response{}, false, fmt.Errorf("idempotency: claim key: %w", err)
	}

	resp, err := fn(ctx)
	if err != nil {
		return idempotency.Response{}, false, err
	}
	if err := complete(ctx, tx, req, uuid.Nil, resp); err != nil {
		return idempotency.Response{}, false, err
	}
	return resp, false, nil
}

// reserveSQL is claimSQL for a reservation taken outside a transaction: the
// row commits as in progress with a lease (locked_until) and a reservation
// token. Besides an expired row, it also takes over a reservation whose lease
// passed without a stored response (its request died), provided the
// fingerprint matches; a different fingerprint stays a reused key.
const reserveSQL = `
    INSERT INTO idempotency_key (scope, key, fingerprint, status, expires_at, locked_until, reservation)
    VALUES ($1, $2, $3, 'in_progress', now() + $4 * interval '1 microsecond', now() + $5 * interval '1 microsecond', $6)
    ON CONFLICT (scope, key) DO UPDATE
       SET fingerprint = EXCLUDED.fingerprint, status = 'in_progress',
           response_status = NULL, response_header = NULL, response_body = NULL,
           created_at = now(), locked_until = EXCLUDED.locked_until, reservation = EXCLUDED.reservation,
           expires_at = EXCLUDED.expires_at
     WHERE idempotency_key.expires_at <= now()
        OR (idempotency_key.status = 'in_progress'
            AND idempotency_key.locked_until <= now()
            AND idempotency_key.fingerprint = EXCLUDED.fingerprint)
    RETURNING true`

// Reserve claims req's key for lease, outside any transaction of the
// operation: the HTTP middleware's path. The returned Reservation has Token
// set when this request now holds the key (new, expired, or a lapsed lease of
// the same request) and must run its operation, then call Complete or
// Release with that token; it has Replay set when the operation already
// finished. A key held by a live lease fails with ErrInProgress, a key
// claimed with another fingerprint with ErrKeyReused, an invalid req with
// ErrInvalidRequest. A lease <= 0 is rejected as ErrInvalidRequest: it must
// outlast the operation, or a retry would run it again concurrently.
func (s *Store) Reserve(ctx context.Context, db pgxtx.DBTX, req idempotency.Request, lease time.Duration) (idempotency.Reservation, error) {
	if err := req.Validate(); err != nil {
		return idempotency.Reservation{}, err
	}
	if lease <= 0 {
		return idempotency.Reservation{}, fmt.Errorf("%w: lease must be positive", idempotency.ErrInvalidRequest)
	}
	token := uuid.New()
	var owned bool
	err := db.QueryRow(ctx, reserveSQL, req.Scope, req.Key, req.Fingerprint,
		s.ttl.Microseconds(), lease.Microseconds(), token).Scan(&owned)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		resp, _, err := s.existing(ctx, db, req)
		if err != nil {
			return idempotency.Reservation{}, err
		}
		return idempotency.Reservation{Replay: &resp}, nil
	case err != nil:
		return idempotency.Reservation{}, fmt.Errorf("idempotency: reserve key: %w", err)
	}
	return idempotency.Reservation{Token: token}, nil
}

// Complete stores resp as the response of req's key, held by the
// reservation token. It fails when the reservation is no longer this
// request's: its lease lapsed and another request took the key over, or the
// row expired and was reclaimed.
func (s *Store) Complete(ctx context.Context, db pgxtx.DBTX, req idempotency.Request, token uuid.UUID, resp idempotency.Response) error {
	return complete(ctx, db, req, token, resp)
}

// Release deletes req's in-progress reservation held by token, so a retry
// runs the operation again. It is a no-op when the reservation is no longer
// this request's.
func (s *Store) Release(ctx context.Context, db pgxtx.DBTX, req idempotency.Request, token uuid.UUID) error {
	if _, err := db.Exec(ctx, `
        DELETE FROM idempotency_key
        WHERE scope = $1 AND key = $2 AND status = 'in_progress' AND reservation = $3`,
		req.Scope, req.Key, token); err != nil {
		return fmt.Errorf("idempotency: release key: %w", err)
	}
	return nil
}

// existing reads the row another transaction committed for req's key.
func (s *Store) existing(ctx context.Context, db pgxtx.DBTX, req idempotency.Request) (idempotency.Response, bool, error) {
	var (
		fingerprint []byte
		status      string
		code        *int
		header      []byte
		body        []byte
	)
	err := db.QueryRow(ctx, `
        SELECT fingerprint, status, response_status, response_header, response_body
        FROM idempotency_key WHERE scope = $1 AND key = $2`, req.Scope, req.Key,
	).Scan(&fingerprint, &status, &code, &header, &body)
	if err != nil {
		return idempotency.Response{}, false, fmt.Errorf("idempotency: read key: %w", err)
	}
	if !bytes.Equal(fingerprint, req.Fingerprint) {
		return idempotency.Response{}, false, idempotency.ErrKeyReused
	}
	if status != "completed" || code == nil {
		return idempotency.Response{}, false, idempotency.ErrInProgress
	}
	resp := idempotency.Response{Status: *code, Body: body}
	if header != nil {
		if err := json.Unmarshal(header, &resp.Header); err != nil {
			return idempotency.Response{}, false, fmt.Errorf("idempotency: decode stored header: %w", err)
		}
	}
	return resp, true, nil
}

// complete stores resp as the response of req's key and marks it completed.
// token is the reservation the caller holds; uuid.Nil means the key was
// claimed by Do inside the caller's transaction, which takes no reservation.
func complete(ctx context.Context, db pgxtx.DBTX, req idempotency.Request, token uuid.UUID, resp idempotency.Response) error {
	var header []byte
	if resp.Header != nil {
		var err error
		if header, err = json.Marshal(resp.Header); err != nil {
			return fmt.Errorf("idempotency: encode header: %w", err)
		}
	}
	tag, err := db.Exec(ctx, `
        UPDATE idempotency_key
           SET status = 'completed', response_status = $4, response_header = $5, response_body = $6,
               locked_until = NULL, reservation = NULL
         WHERE scope = $1 AND key = $2 AND fingerprint = $3 AND status = 'in_progress'
           AND reservation IS NOT DISTINCT FROM $7`,
		req.Scope, req.Key, req.Fingerprint, resp.Status, header, resp.Body, nullToken(token))
	if err != nil {
		return fmt.Errorf("idempotency: store response: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("idempotency: store response: key %q is no longer reserved by this request", req.Key)
	}
	return nil
}

// DeleteExpired deletes at most limit expired rows (oldest expiry first) and
// returns how many it deleted. A limit <= 0 means 1000. A periodic job of the
// consumer calls it until it returns fewer than limit; keeping each call
// bounded keeps it from holding locks on a large table.
func (s *Store) DeleteExpired(ctx context.Context, db pgxtx.DBTX, limit int) (int64, error) {
	if limit <= 0 {
		limit = defaultDeleteLimit
	}
	tag, err := db.Exec(ctx, `
        DELETE FROM idempotency_key
        WHERE ctid IN (
            SELECT ctid FROM idempotency_key
            WHERE expires_at <= now()
            ORDER BY expires_at
            LIMIT $1)`, limit)
	if err != nil {
		return 0, fmt.Errorf("idempotency: delete expired keys: %w", err)
	}
	return tag.RowsAffected(), nil
}

// nullToken stores uuid.Nil as NULL: Do's claims carry no reservation.
func nullToken(token uuid.UUID) *uuid.UUID {
	if token == uuid.Nil {
		return nil
	}
	return &token
}
