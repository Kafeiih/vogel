package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kafeiih/vogel/pgxtx"
)

// ErrDocumentNotFound is returned by DocumentStore.GetByID when no row
// matches the given id. Check for it with errors.Is.
var ErrDocumentNotFound = errors.New("document store: not found")

// DocumentStore is a stateless PostgreSQL-backed store for Document,
// mirroring the shape of workflow/postgres.Repository: every method takes a
// pgxtx.DBTX as its second argument instead of the store holding a
// *pgxpool.Pool itself.
//
// Taking pgxtx.DBTX (rather than a pool) is what lets the CALLER decide
// whether a given call runs inside a transaction: pgxtx.DBFromContext
// returns the pool when no transaction is active on the given context, and
// the live pgx.Tx when one is (see pgxtx.PgxTxManager.WithTx). Because of
// that, DocumentStore never needs two variants of each method -- one for
// "inside a transaction" and one for "standalone".
type DocumentStore struct{}

// NewDocumentStore creates a stateless DocumentStore.
func NewDocumentStore() *DocumentStore {
	return &DocumentStore{}
}

const documentColumns = "id, title, kind, issued_at, storage_key, owner_id, created_at"

// Create inserts a new document.
func (s *DocumentStore) Create(ctx context.Context, db pgxtx.DBTX, d *Document) error {
	_, err := db.Exec(ctx, `
		INSERT INTO documents (`+documentColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		d.ID, d.Title, d.Kind, d.IssuedAt, d.StorageKey, d.OwnerID, d.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("document store: create %s: %w", d.ID, err)
	}
	return nil
}

// GetByID retrieves a single document by its primary key. It returns
// ErrDocumentNotFound (checkable via errors.Is) when no row matches.
func (s *DocumentStore) GetByID(ctx context.Context, db pgxtx.DBTX, id uuid.UUID) (*Document, error) {
	row := db.QueryRow(ctx, `SELECT `+documentColumns+` FROM documents WHERE id = $1`, id)

	var d Document
	err := row.Scan(&d.ID, &d.Title, &d.Kind, &d.IssuedAt, &d.StorageKey, &d.OwnerID, &d.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("document store: get %s: %w", id, ErrDocumentNotFound)
		}
		return nil, fmt.Errorf("document store: get %s: %w", id, err)
	}
	return &d, nil
}

// List returns a page of documents ordered by created_at descending, plus
// the total matching row count -- computed in the SAME query via
// count(*) OVER(), the same technique audit/postgres.Repository.List uses to
// avoid a second round trip just for pagination metadata.
func (s *DocumentStore) List(ctx context.Context, db pgxtx.DBTX, limit, offset int) ([]Document, int, error) {
	rows, err := db.Query(ctx, `
		SELECT `+documentColumns+`, count(*) OVER()::INT
		FROM documents
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2`,
		limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("document store: list: %w", err)
	}
	defer rows.Close()

	docs := make([]Document, 0, limit)
	total := 0
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.Title, &d.Kind, &d.IssuedAt, &d.StorageKey, &d.OwnerID, &d.CreatedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("document store: list scan: %w", err)
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("document store: list iterate: %w", err)
	}
	return docs, total, nil
}

// UpdateStorageKey updates the stored object key for id, e.g. after a
// replacement upload.
func (s *DocumentStore) UpdateStorageKey(ctx context.Context, db pgxtx.DBTX, id uuid.UUID, key string) error {
	_, err := db.Exec(ctx, `UPDATE documents SET storage_key = $1 WHERE id = $2`, key, id)
	if err != nil {
		return fmt.Errorf("document store: update storage key %s: %w", id, err)
	}
	return nil
}

// Delete removes a document by ID. See DocumentHandler.Delete for why this
// route exists: it is the one that demonstrates access.Guard's per-instance
// check.
func (s *DocumentStore) Delete(ctx context.Context, db pgxtx.DBTX, id uuid.UUID) error {
	_, err := db.Exec(ctx, `DELETE FROM documents WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("document store: delete %s: %w", id, err)
	}
	return nil
}

// OwnedIDs returns the string IDs of every document whose owner_id is
// ownerID.
//
// This stands in for "the user's current assignments looked up in another
// system" from the access package's PrincipalAttributes doc comment: in a
// real deployment this would more plausibly be a call to an assignment
// service or a different bounded context, not a query against the same
// table the handler is about to load from -- but a second in-memory system
// would just be this same query with extra ceremony, and the point this
// example makes does not depend on where the lookup happens.
func (s *DocumentStore) OwnedIDs(ctx context.Context, db pgxtx.DBTX, ownerID string) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT id FROM documents WHERE owner_id = $1`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("document store: owned ids for %s: %w", ownerID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("document store: owned ids scan: %w", err)
		}
		ids = append(ids, id.String())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("document store: owned ids iterate: %w", err)
	}
	return ids, nil
}
