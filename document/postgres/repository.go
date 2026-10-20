// Package postgres provides the PostgreSQL-backed implementation of the
// document.Repository port, built on native pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kafeiih/vogel/document"
	"github.com/kafeiih/vogel/pgxtx"
)

const documentColumns = `id, domain, external_id, case_id, template_name, template_version,
    sha256, storage_key, verification_key, size_bytes, created_at`

// Repository implements document.Repository using pgx. It is stateless:
// every method receives the pgxtx.DBTX to run against, so the caller decides
// whether to use a plain pool connection or an existing transaction.
type Repository struct{}

var _ document.Repository = (*Repository)(nil)

// NewRepository creates a stateless PostgreSQL-backed document repository.
func NewRepository() *Repository {
	return &Repository{}
}

// rowScanner is the subset of pgx.Row and pgx.Rows this package needs.
type rowScanner interface {
	Scan(dest ...any) error
}

// Insert persists d. A duplicate id, storage key or verification key is
// returned as the driver's unique-violation error, wrapped.
func (r *Repository) Insert(ctx context.Context, db pgxtx.DBTX, d *document.Document) error {
	_, err := db.Exec(ctx, `
        INSERT INTO document (`+documentColumns+`)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		d.ID, d.Domain, d.ExternalID, d.CaseID, d.TemplateName, d.TemplateVersion,
		d.SHA256, d.StorageKey, d.VerificationKey, d.Size, d.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("document: insert: %w", err)
	}
	return nil
}

// GetByID retrieves a single document by its primary key.
func (r *Repository) GetByID(ctx context.Context, db pgxtx.DBTX, id uuid.UUID) (*document.Document, error) {
	row := db.QueryRow(ctx, `SELECT `+documentColumns+` FROM document WHERE id = $1`, id)
	d, err := scanDocument(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("document: get: %w", document.ErrNotFound)
		}
		return nil, fmt.Errorf("document: get: %w", err)
	}
	return d, nil
}

// ListByCase returns every document of caseID ordered by created_at, then
// id, ascending.
func (r *Repository) ListByCase(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]document.Document, error) {
	rows, err := db.Query(ctx, `SELECT `+documentColumns+` FROM document WHERE case_id = $1 ORDER BY created_at ASC, id ASC`, caseID)
	if err != nil {
		return nil, fmt.Errorf("document: list by case: %w", err)
	}
	defer rows.Close()

	var docs []document.Document
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("document: list by case: %w", err)
		}
		docs = append(docs, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("document: list by case: %w", err)
	}
	return docs, nil
}

// GetByVerificationKey retrieves the document whose verification key is key.
func (r *Repository) GetByVerificationKey(ctx context.Context, db pgxtx.DBTX, key string) (*document.Document, error) {
	row := db.QueryRow(ctx, `SELECT `+documentColumns+` FROM document WHERE verification_key = $1`, key)
	d, err := scanDocument(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("document: get by verification key: %w", document.ErrNotFound)
		}
		return nil, fmt.Errorf("document: get by verification key: %w", err)
	}
	return d, nil
}

func scanDocument(row rowScanner) (*document.Document, error) {
	var d document.Document
	if err := row.Scan(
		&d.ID, &d.Domain, &d.ExternalID, &d.CaseID, &d.TemplateName, &d.TemplateVersion,
		&d.SHA256, &d.StorageKey, &d.VerificationKey, &d.Size, &d.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &d, nil
}
