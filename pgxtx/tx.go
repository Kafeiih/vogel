// Package pgxtx provides pgx-based transaction plumbing: a context-scoped
// active transaction, and a WithTx helper that runs a callback inside one
// transaction, committing on success and rolling back on error or panic.
//
// This package holds transaction plumbing, not repositories — hence the name
// (it was called "repository" in its original home).
//
// pgxtx lives at the module root, as a sibling of the postgres package rather
// than nested under it: PgxTxManager.WithTx works against any *pgxpool.Pool
// the consumer built themselves — including one built by their own code, by
// go-crucible's or go-licencias' historical constructor, or by a test harness
// — so this package must never imply a dependency on this library's own
// postgres.NewPool.
package pgxtx

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ctxKey struct{}

// DBTX is the common interface satisfied by both pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// dbFromContext returns the active transaction from context, or falls back to pool.
func dbFromContext(ctx context.Context, pool *pgxpool.Pool) DBTX {
	if tx, ok := ctx.Value(ctxKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}

// DBFromContext returns the active transaction from context, or falls back to pool.
// Exported for use by application-layer code that needs to run queries within
// whatever transaction (if any) is active on ctx.
func DBFromContext(ctx context.Context, pool *pgxpool.Pool) DBTX {
	return dbFromContext(ctx, pool)
}

// TxFromContext extracts the raw pgx.Tx from context, or nil if not in a transaction.
// Used by adapters (e.g. a queue) that need to enqueue work within the same transaction.
func TxFromContext(ctx context.Context) pgx.Tx {
	if tx, ok := ctx.Value(ctxKey{}).(pgx.Tx); ok {
		return tx
	}
	return nil
}

// txBeginner is the subset of *pgxpool.Pool that PgxTxManager needs. It exists
// so tests can substitute a fake transaction source without a real database.
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// PgxTxManager runs callbacks inside a pgx transaction.
type PgxTxManager struct {
	pool txBeginner
}

// NewPgxTxManager creates a PgxTxManager backed by pool.
func NewPgxTxManager(pool *pgxpool.Pool) *PgxTxManager {
	return &PgxTxManager{pool: pool}
}

// WithTx begins a transaction, stores it in the context passed to fn, and:
//   - commits if fn returns nil,
//   - rolls back if fn returns an error,
//   - rolls back and re-panics if fn panics.
//
// A panic inside fn is never swallowed: it propagates to the caller after the
// transaction has been rolled back, so a panicking callback can never leak an
// open transaction holding its locks until pool eviction.
func (m *PgxTxManager) WithTx(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	txCtx := context.WithValue(ctx, ctxKey{}, tx)

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
	}()

	if err = fn(txCtx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}

	return tx.Commit(ctx)
}
