// Package postgres provides the PostgreSQL-backed implementation of the
// workflow.Repository port, built on native pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"
)

const caseColumns = `id, definition, version, domain, external_id, unit, state, status,
    assigned_to, opened_at, closed_at, deadline_at`

const eventColumns = `id, case_id, seq, kind, from_state, to_state, action, actor_id, occurred_at`

// uniqueViolationCode is the PostgreSQL error code for a unique constraint
// violation (23505).
const uniqueViolationCode = "23505"

// Repository implements workflow.Repository using pgx. It is stateless:
// every method receives the pgxtx.DBTX to run against, so the caller decides
// whether to use a plain pool connection or an existing transaction.
type Repository struct{}

var _ workflow.Repository = (*Repository)(nil)

// NewRepository creates a stateless PostgreSQL-backed workflow repository.
func NewRepository() *Repository {
	return &Repository{}
}

// rowScanner is the subset of pgx.Row and pgx.Rows this package needs, so a
// single scan function can serve both a QueryRow result and a Query row.
type rowScanner interface {
	Scan(dest ...any) error
}

// Create inserts a new case. A unique-violation on (domain, external_id) is
// mapped to workflow.ErrCaseExists.
func (r *Repository) Create(ctx context.Context, db pgxtx.DBTX, c *workflow.Case) error {
	_, err := db.Exec(ctx, `
        INSERT INTO workflow_case (`+caseColumns+`)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		c.ID, c.Definition, c.Version, c.Domain, c.ExternalID, c.Unit, c.State, string(c.Status),
		c.AssignedTo, c.OpenedAt, c.ClosedAt, c.DeadlineAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode {
			return fmt.Errorf("workflow: create case: %w", workflow.ErrCaseExists)
		}
		return fmt.Errorf("workflow: create case: %w", err)
	}
	return nil
}

// GetByID retrieves a single case by its primary key.
func (r *Repository) GetByID(ctx context.Context, db pgxtx.DBTX, id uuid.UUID) (*workflow.Case, error) {
	row := db.QueryRow(ctx, `SELECT `+caseColumns+` FROM workflow_case WHERE id = $1`, id)
	c, err := scanCase(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("workflow: get case: %w", workflow.ErrCaseNotFound)
		}
		return nil, fmt.Errorf("workflow: get case: %w", err)
	}
	return c, nil
}

// GetByExternalID retrieves a single case by its (domain, external_id) pair.
func (r *Repository) GetByExternalID(ctx context.Context, db pgxtx.DBTX, domain, externalID string) (*workflow.Case, error) {
	row := db.QueryRow(ctx, `SELECT `+caseColumns+` FROM workflow_case WHERE domain = $1 AND external_id = $2`, domain, externalID)
	c, err := scanCase(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("workflow: get case by external id: %w", workflow.ErrCaseNotFound)
		}
		return nil, fmt.Errorf("workflow: get case by external id: %w", err)
	}
	return c, nil
}

// Update persists every mutable field of c.
func (r *Repository) Update(ctx context.Context, db pgxtx.DBTX, c *workflow.Case) error {
	tag, err := db.Exec(ctx, `
        UPDATE workflow_case SET
            definition = $2, version = $3, domain = $4, external_id = $5, unit = $6,
            state = $7, status = $8, assigned_to = $9, opened_at = $10, closed_at = $11, deadline_at = $12
        WHERE id = $1`,
		c.ID, c.Definition, c.Version, c.Domain, c.ExternalID, c.Unit,
		c.State, string(c.Status), c.AssignedTo, c.OpenedAt, c.ClosedAt, c.DeadlineAt,
	)
	if err != nil {
		return fmt.Errorf("workflow: update case: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("workflow: update case: %w", workflow.ErrCaseNotFound)
	}
	return nil
}

// AppendEvent inserts e, computing its Seq in SQL as
// COALESCE(MAX(seq), 0) + 1 for the case so concurrent appends cannot
// silently collide — the UNIQUE(case_id, seq) constraint is the backstop.
// e.Seq is overwritten with the value actually persisted.
func (r *Repository) AppendEvent(ctx context.Context, db pgxtx.DBTX, e *workflow.Event) error {
	row := db.QueryRow(ctx, `
        INSERT INTO workflow_event (id, case_id, seq, kind, from_state, to_state, action, actor_id, occurred_at)
        VALUES ($1, $2,
            COALESCE((SELECT MAX(seq) FROM workflow_event WHERE case_id = $2), 0) + 1,
            $3, $4, $5, $6, $7, $8)
        RETURNING seq`,
		e.ID, e.CaseID, string(e.Kind), e.FromState, e.ToState, e.Action, e.ActorID, e.OccurredAt,
	)

	var seq int64
	if err := row.Scan(&seq); err != nil {
		return fmt.Errorf("workflow: append event: %w", err)
	}
	e.Seq = seq
	return nil
}

// ListEvents returns every event for caseID, ordered by seq ascending.
func (r *Repository) ListEvents(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]workflow.Event, error) {
	rows, err := db.Query(ctx, `SELECT `+eventColumns+` FROM workflow_event WHERE case_id = $1 ORDER BY seq ASC`, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: list events: %w", err)
	}
	defer rows.Close()

	var events []workflow.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("workflow: list events: %w", err)
		}
		events = append(events, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workflow: list events: %w", err)
	}
	return events, nil
}

// ListByEligibility returns open cases matching f. States, when non-empty,
// restricts results to cases whose current node is one of those IDs — the
// mapping from Position/Unit to that set of node IDs is resolved by the
// caller (see workflow.Engine.EligibilityFor), not by this query.
func (r *Repository) ListByEligibility(ctx context.Context, db pgxtx.DBTX, f workflow.InboxFilter) ([]workflow.Case, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}

	const q = `
        SELECT ` + caseColumns + `
        FROM workflow_case
        WHERE status = 'open'
          AND ($1::TEXT    IS NULL OR domain = $1)
          AND ($2::TEXT[]  IS NULL OR state = ANY($2))
          AND ($3::TEXT    IS NULL OR assigned_to = $3)
          AND (NOT $4::BOOLEAN OR assigned_to = '')
          AND (NOT $5::BOOLEAN OR (deadline_at IS NOT NULL AND deadline_at < now()))
        ORDER BY opened_at ASC
        LIMIT $6 OFFSET $7`

	rows, err := db.Query(ctx, q,
		nullString(f.Domain),
		f.States,
		nullString(f.AssignedTo),
		f.Unassigned,
		f.Overdue,
		limit,
		f.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("workflow: list by eligibility: %w", err)
	}
	defer rows.Close()

	var cases []workflow.Case
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, fmt.Errorf("workflow: list by eligibility: %w", err)
		}
		cases = append(cases, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workflow: list by eligibility: %w", err)
	}
	return cases, nil
}

func scanCase(row rowScanner) (*workflow.Case, error) {
	var c workflow.Case
	var status string
	if err := row.Scan(
		&c.ID, &c.Definition, &c.Version, &c.Domain, &c.ExternalID, &c.Unit, &c.State, &status,
		&c.AssignedTo, &c.OpenedAt, &c.ClosedAt, &c.DeadlineAt,
	); err != nil {
		return nil, err
	}
	c.Status = workflow.Status(status)
	return &c, nil
}

func scanEvent(row rowScanner) (*workflow.Event, error) {
	var e workflow.Event
	var kind string
	if err := row.Scan(
		&e.ID, &e.CaseID, &e.Seq, &kind, &e.FromState, &e.ToState, &e.Action, &e.ActorID, &e.OccurredAt,
	); err != nil {
		return nil, err
	}
	e.Kind = workflow.EventKind(kind)
	return &e, nil
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
