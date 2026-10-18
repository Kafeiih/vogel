package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// documentField is one column a document is read from: the SQL expression
// selected and the Document field it is scanned into.
type documentField struct {
	expr string
	dest any
}

// documentFields is the single ordered list of what a document read selects
// and where each value lands. selectColumns and scanDocument are both built
// from it, so the SELECT list and the Scan destinations cannot drift apart.
// Nullable text columns are read as "" with COALESCE.
func documentFields(d *Document) []documentField {
	return []documentField{
		{"id", &d.ID},
		{"title", &d.Title},
		{"kind", &d.Kind},
		{"issued_at", &d.IssuedAt},
		{"storage_key", &d.StorageKey},
		{"file_status", &d.FileStatus},
		{"COALESCE(file_content_type, '')", &d.FileContentType},
		{"COALESCE(previous_storage_key, '')", &d.PreviousStorageKey},
		{"COALESCE(previous_content_type, '')", &d.PreviousContentType},
		{"owner_id", &d.OwnerID},
		{"created_at", &d.CreatedAt},
	}
}

// selectColumns is the SELECT list matching documentFields.
var selectColumns = func() string {
	fields := documentFields(&Document{})
	exprs := make([]string, len(fields))
	for i, f := range fields {
		exprs[i] = f.expr
	}
	return strings.Join(exprs, ", ")
}()

// scanDocument scans one row selected with selectColumns into d, followed by
// any extra destinations (for example a window count).
func scanDocument(row pgx.Row, d *Document, extra ...any) error {
	fields := documentFields(d)
	dest := make([]any, 0, len(fields)+len(extra))
	for _, f := range fields {
		dest = append(dest, f.dest)
	}
	return row.Scan(append(dest, extra...)...)
}

// insertColumns are the columns Create writes, in the order of its VALUES
// arguments right below; the file lifecycle columns other than file_status
// keep their defaults.
const insertColumns = "id, title, kind, issued_at, storage_key, file_status, owner_id, created_at"

// Create inserts a new document.
func (s *DocumentStore) Create(ctx context.Context, db pgxtx.DBTX, d *Document) error {
	_, err := db.Exec(ctx, `
		INSERT INTO documents (`+insertColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		d.ID, d.Title, d.Kind, d.IssuedAt, d.StorageKey, d.FileStatus, d.OwnerID, d.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("document store: create %s: %w", d.ID, err)
	}
	return nil
}

// GetByID retrieves a single document by its primary key. It returns
// ErrDocumentNotFound (checkable via errors.Is) when no row matches.
func (s *DocumentStore) GetByID(ctx context.Context, db pgxtx.DBTX, id uuid.UUID) (*Document, error) {
	row := db.QueryRow(ctx, `SELECT `+selectColumns+` FROM documents WHERE id = $1`, id)

	var d Document
	err := scanDocument(row, &d)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("document store: get %s: %w", id, ErrDocumentNotFound)
		}
		return nil, fmt.Errorf("document store: get %s: %w", id, err)
	}
	return &d, nil
}

// GetByIDForUpdate is GetByID with the row locked (SELECT ... FOR UPDATE)
// until the transaction db belongs to ends. Use it inside a transaction
// that decides what to write from the row it read: a concurrent writer then
// either commits first, and this read sees its result, or waits for this
// transaction. Outside a transaction the lock is released at once.
func (s *DocumentStore) GetByIDForUpdate(ctx context.Context, db pgxtx.DBTX, id uuid.UUID) (*Document, error) {
	row := db.QueryRow(ctx, `SELECT `+selectColumns+` FROM documents WHERE id = $1 FOR UPDATE`, id)

	var d Document
	if err := scanDocument(row, &d); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("document store: get %s for update: %w", id, ErrDocumentNotFound)
		}
		return nil, fmt.Errorf("document store: get %s for update: %w", id, err)
	}
	return &d, nil
}

// List returns a page of documents ordered by created_at descending, plus
// the total matching row count -- computed in the SAME query via
// count(*) OVER(), the same technique audit/postgres.Repository.List uses to
// avoid a second round trip just for pagination metadata.
func (s *DocumentStore) List(ctx context.Context, db pgxtx.DBTX, limit, offset int) ([]Document, int, error) {
	rows, err := db.Query(ctx, `
		SELECT `+selectColumns+`, count(*) OVER()::INT
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
		if err := scanDocument(rows, &d, &total); err != nil {
			return nil, 0, fmt.Errorf("document store: list scan: %w", err)
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("document store: list iterate: %w", err)
	}
	return docs, total, nil
}

// AttachPendingFile points id at the uploaded object key and marks its file
// pending validation, clearing any previously validated content type: the
// new object has not been sniffed yet. previousKey and previousContentType
// ("" stores NULL) are the ready file this upload would replace, kept on
// the row so downloads can serve it meanwhile and a rejection can restore
// it. A later confirm replaces the key, so a validation job still running
// for the old key no longer matches it (see ResolveUpload).
func (s *DocumentStore) AttachPendingFile(ctx context.Context, db pgxtx.DBTX, id uuid.UUID, key, previousKey, previousContentType string) error {
	_, err := db.Exec(ctx, `
		UPDATE documents
		SET storage_key = $1, file_status = 'pending', file_content_type = NULL,
		    previous_storage_key = NULLIF($2, ''), previous_content_type = NULLIF($3, '')
		WHERE id = $4`, key, previousKey, previousContentType, id)
	if err != nil {
		return fmt.Errorf("document store: attach pending file %s: %w", id, err)
	}
	return nil
}

// ResolveUpload records validate_upload's verdict on uploadKey: it moves the
// row to storageKey (the final file key for an accepted upload, the
// previous file for a restore, uploadKey itself for a plain rejection) with
// status and the sniffed contentType ("" stores NULL), and clears the
// previous file, which no longer has anything pending to stand in for. It
// changes the row only while its storage_key is still uploadKey AND its
// file is still pending, and reports whether it did: a stale job -- one
// whose key a newer confirm already replaced -- never overwrites the newer
// upload's state, and a retried job whose earlier attempt already resolved
// the row changes nothing and audits nothing.
func (s *DocumentStore) ResolveUpload(ctx context.Context, db pgxtx.DBTX, id uuid.UUID, uploadKey, storageKey, status, contentType string) (bool, error) {
	tag, err := db.Exec(ctx, `
		UPDATE documents
		SET storage_key = $1, file_status = $2, file_content_type = NULLIF($3, ''),
		    previous_storage_key = NULL, previous_content_type = NULL
		WHERE id = $4 AND storage_key = $5 AND file_status = 'pending'`, storageKey, status, contentType, id, uploadKey)
	if err != nil {
		return false, fmt.Errorf("document store: resolve upload %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
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
