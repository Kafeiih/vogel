// Package postgres provides the PostgreSQL-backed implementation of the
// audit.Repository port, built on pgx.
//
// It is ported from go-licencias' audit_postgres.go rather than go-crucible's
// or go-bluprint's: go-licencias' List runs a single query using
// count(*) OVER() to get the total alongside the page of results, instead of
// the two round trips (one COUNT(*), one SELECT) the other two systems each
// issue; and go-licencias types the resource-ID filter as *uuid.UUID instead
// of a bare string, so an invalid ID is rejected by the caller's own parsing
// instead of silently matching zero rows deep inside a query.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/pgxtx"
)

const (
	queryReadTimeout  = 5 * time.Second
	queryWriteTimeout = 10 * time.Second
)

const auditColumns = `id, resource_type, resource_id, aggregate_type, aggregate_id,
	operation_category, operation_name, status,
	actor_id, username, source, request_id, ip, user_agent,
	object_repr,
	input_payload, before_snapshot, after_snapshot, changed_fields, affected_resources,
	error_code, error_message,
	created_at`

// Repository implements audit.Repository using pgxpool.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a new PostgreSQL-backed audit repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Create inserts an audit entry using the given db (either a live pgx.Tx or
// the pool itself — see vogel/pgxtx.DBTX).
func (r *Repository) Create(ctx context.Context, db pgxtx.DBTX, entry *audit.Entry) error {
	ctx, cancel := context.WithTimeout(ctx, queryWriteTimeout)
	defer cancel()

	inputPayload, err := jsonOrNull(entry.InputPayload)
	if err != nil {
		return fmt.Errorf("marshal input_payload: %w", err)
	}
	beforeSnapshot, err := jsonOrNull(entry.BeforeSnapshot)
	if err != nil {
		return fmt.Errorf("marshal before_snapshot: %w", err)
	}
	afterSnapshot, err := jsonOrNull(entry.AfterSnapshot)
	if err != nil {
		return fmt.Errorf("marshal after_snapshot: %w", err)
	}
	changedFields, err := jsonOrNull(entry.ChangedFields)
	if err != nil {
		return fmt.Errorf("marshal changed_fields: %w", err)
	}
	affectedResources, err := jsonSliceOrNull(entry.AffectedResources)
	if err != nil {
		return fmt.Errorf("marshal affected_resources: %w", err)
	}

	_, err = db.Exec(ctx, `
		INSERT INTO audit_log (`+auditColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)`,
		entry.ID, entry.ResourceType, entry.ResourceID, entry.AggregateType, entry.AggregateID,
		entry.OperationCategory, entry.OperationName, entry.Status,
		entry.ActorID, entry.Username, entry.Source, entry.RequestID, entry.IP, entry.UserAgent,
		entry.ObjectRepr,
		inputPayload, beforeSnapshot, afterSnapshot, changedFields, affectedResources,
		entry.ErrorCode, entry.ErrorMessage,
		entry.CreatedAt,
	)
	return err
}

// GetByID retrieves a single audit entry by its primary key.
func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*audit.Entry, error) {
	ctx, cancel := context.WithTimeout(ctx, queryReadTimeout)
	defer cancel()

	row := r.pool.QueryRow(ctx,
		"SELECT "+auditColumns+" FROM audit_log WHERE id = $1", id)
	return scanEntry(row)
}

// List returns a filtered, paginated slice of audit entries plus the total
// matching count, using a single SQL statement with count(*) OVER()::INT
// instead of a separate COUNT(*) round trip.
func (r *Repository) List(ctx context.Context, f audit.Filters) ([]audit.Entry, int, error) {
	ctx, cancel := context.WithTimeout(ctx, queryReadTimeout)
	defer cancel()

	const q = `
		SELECT ` + auditColumns + `,
		       count(*) OVER()::INT AS total
		FROM audit_log
		WHERE ($1::TEXT        IS NULL OR actor_id           = $1)
		  AND ($2::TEXT        IS NULL OR resource_type       = $2)
		  AND ($3::TEXT        IS NULL OR resource_id         = $3)
		  AND ($4::TEXT        IS NULL OR operation_category  = $4)
		  AND ($5::TEXT        IS NULL OR operation_name      = $5)
		  AND ($6::TEXT        IS NULL OR status              = $6)
		  AND ($7::TEXT        IS NULL OR aggregate_type      = $7)
		  AND ($8::TEXT        IS NULL OR aggregate_id        = $8)
		  AND ($9::TIMESTAMPTZ  IS NULL OR created_at >= $9)
		  AND ($10::TIMESTAMPTZ IS NULL OR created_at <= $10)
		ORDER BY created_at DESC
		LIMIT $11 OFFSET $12`

	// resource_id is a TEXT column; f.ResourceID is typed *uuid.UUID so an
	// invalid ID is rejected by the caller's own parsing rather than reaching
	// this query at all. Render it to its string form (or nil) here.
	var resourceIDStr *string
	if f.ResourceID != nil {
		s := f.ResourceID.String()
		resourceIDStr = &s
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}

	rows, err := r.pool.Query(ctx, q,
		nullString(f.ActorID),
		nullString(f.ResourceType),
		resourceIDStr,
		nullString(f.OperationCategory),
		nullString(f.OperationName),
		nullString(f.Status),
		nullString(f.AggregateType),
		nullString(f.AggregateID),
		f.From,
		f.To,
		limit,
		f.Offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit_log: %w", err)
	}
	defer rows.Close()

	var entries []audit.Entry
	total := 0

	for rows.Next() {
		e, t, err := scanEntryWithTotal(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan audit_log row: %w", err)
		}
		entries = append(entries, *e)
		total = t
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list audit_log iterate: %w", err)
	}

	return entries, total, nil
}

func scanEntry(row pgx.Row) (*audit.Entry, error) {
	var e audit.Entry
	var inputPayload, beforeSnapshot, afterSnapshot, changedFields, affectedResources []byte

	if err := row.Scan(
		&e.ID, &e.ResourceType, &e.ResourceID, &e.AggregateType, &e.AggregateID,
		&e.OperationCategory, &e.OperationName, &e.Status,
		&e.ActorID, &e.Username, &e.Source, &e.RequestID, &e.IP, &e.UserAgent,
		&e.ObjectRepr,
		&inputPayload, &beforeSnapshot, &afterSnapshot, &changedFields, &affectedResources,
		&e.ErrorCode, &e.ErrorMessage,
		&e.CreatedAt,
	); err != nil {
		return nil, err
	}

	if err := unmarshalJSONFields(&e, inputPayload, beforeSnapshot, afterSnapshot, changedFields, affectedResources); err != nil {
		return nil, err
	}

	return &e, nil
}

// scanEntryWithTotal scans a row that includes the window-function total
// column appended after the shared audit columns.
func scanEntryWithTotal(rows pgx.Rows) (*audit.Entry, int, error) {
	var e audit.Entry
	var inputPayload, beforeSnapshot, afterSnapshot, changedFields, affectedResources []byte
	var total int

	if err := rows.Scan(
		&e.ID, &e.ResourceType, &e.ResourceID, &e.AggregateType, &e.AggregateID,
		&e.OperationCategory, &e.OperationName, &e.Status,
		&e.ActorID, &e.Username, &e.Source, &e.RequestID, &e.IP, &e.UserAgent,
		&e.ObjectRepr,
		&inputPayload, &beforeSnapshot, &afterSnapshot, &changedFields, &affectedResources,
		&e.ErrorCode, &e.ErrorMessage,
		&e.CreatedAt,
		&total,
	); err != nil {
		return nil, 0, err
	}

	if err := unmarshalJSONFields(&e, inputPayload, beforeSnapshot, afterSnapshot, changedFields, affectedResources); err != nil {
		return nil, 0, err
	}

	return &e, total, nil
}

func unmarshalJSONFields(e *audit.Entry, inputPayload, beforeSnapshot, afterSnapshot, changedFields, affectedResources []byte) error {
	if len(inputPayload) > 0 {
		if err := json.Unmarshal(inputPayload, &e.InputPayload); err != nil {
			return fmt.Errorf("unmarshal input_payload: %w", err)
		}
	}
	if len(beforeSnapshot) > 0 {
		if err := json.Unmarshal(beforeSnapshot, &e.BeforeSnapshot); err != nil {
			return fmt.Errorf("unmarshal before_snapshot: %w", err)
		}
	}
	if len(afterSnapshot) > 0 {
		if err := json.Unmarshal(afterSnapshot, &e.AfterSnapshot); err != nil {
			return fmt.Errorf("unmarshal after_snapshot: %w", err)
		}
	}
	if len(changedFields) > 0 {
		if err := json.Unmarshal(changedFields, &e.ChangedFields); err != nil {
			return fmt.Errorf("unmarshal changed_fields: %w", err)
		}
	}
	if len(affectedResources) > 0 {
		if err := json.Unmarshal(affectedResources, &e.AffectedResources); err != nil {
			return fmt.Errorf("unmarshal affected_resources: %w", err)
		}
	}
	return nil
}

func jsonOrNull(m map[string]any) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

func jsonSliceOrNull(s []audit.AffectedResource) ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	return json.Marshal(s)
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
