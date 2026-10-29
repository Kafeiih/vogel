// Package postgres provides the PostgreSQL-backed implementation of the
// workflow.Repository port, built on native pgx.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"
)

const caseColumns = `id, definition, version, domain, external_id, unit, state, status,
    assigned_to, opened_at, closed_at, deadline_at, revision, last_seq, tenant`

const eventColumns = `id, case_id, seq, kind, from_state, to_state, action, actor_id, occurred_at, comment,
    schema_version, detail, tenant, actor_kind, acting_as, on_behalf_of, via_client,
    request_id, trace_id, span_id`

// uniqueViolationCode is the PostgreSQL error code for a unique constraint
// violation (23505).
const uniqueViolationCode = "23505"

// eventSeqConstraint is the name Postgres gave UNIQUE (case_id, seq) on
// workflow_event in migration 001. A violation of it means another writer
// appended that Seq first.
const eventSeqConstraint = "workflow_event_case_id_seq_key"

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

// Create inserts a new case at revision 0 (whatever c.Revision held), with
// c.LastSeq as given, and sets c.Revision to 0. A unique-violation on
// (tenant, domain, external_id) is mapped to workflow.ErrCaseExists.
func (r *Repository) Create(ctx context.Context, db pgxtx.DBTX, c *workflow.Case) error {
	_, err := db.Exec(ctx, `
        INSERT INTO workflow_case (`+caseColumns+`)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 0, $13, $14)`,
		c.ID, c.Definition, c.Version, c.Domain, c.ExternalID, c.Unit, c.State, string(c.Status),
		c.AssignedTo, c.OpenedAt, c.ClosedAt, c.DeadlineAt, c.LastSeq, c.Tenant,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode {
			return fmt.Errorf("workflow: create case: %w", workflow.ErrCaseExists)
		}
		return fmt.Errorf("workflow: create case: %w", err)
	}
	c.Revision = 0
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

// GetByExternalID retrieves a single case by its (tenant, domain, external_id)
// triple. A single-tenant consumer passes an empty tenant.
func (r *Repository) GetByExternalID(ctx context.Context, db pgxtx.DBTX, tenant, domain, externalID string) (*workflow.Case, error) {
	row := db.QueryRow(ctx, `SELECT `+caseColumns+` FROM workflow_case WHERE tenant = $1 AND domain = $2 AND external_id = $3`,
		tenant, domain, externalID)
	c, err := scanCase(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("workflow: get case by external id: %w", workflow.ErrCaseNotFound)
		}
		return nil, fmt.Errorf("workflow: get case by external id: %w", err)
	}
	return c, nil
}

// Update persists every mutable field of c, LastSeq included, as a
// compare-and-swap on c.Revision and on the LastSeq the case was read with
// (expectedLastSeq): the row is written only while its stored revision still
// equals c.Revision and its stored last_seq equals expectedLastSeq, and the
// write advances revision by one. The last_seq check catches a
// workflow.Engine.RecordFailure that took a Seq after the read without
// advancing revision. On success c.Revision is advanced to match, so a caller holding c can keep using it. When no row
// matches, a follow-up existence check tells the two failures apart:
// workflow.ErrCaseNotFound when the case does not exist, workflow.ErrConflict
// when a concurrent writer committed first (nothing is written either way).
//
// Under READ COMMITTED (PostgreSQL's default) a concurrent UPDATE of the same
// row blocks until the first writer commits, then re-evaluates the revision
// predicate and matches zero rows, yielding ErrConflict. Under REPEATABLE
// READ or SERIALIZABLE, PostgreSQL itself aborts the second writer with a
// serialization failure (SQLSTATE 40001) instead; that error is returned
// wrapped as-is and is equally safe to retry.
//
// Rollout: the check only works when EVERY writer of workflow_case bumps
// revision. A binary built against a vogel release that predates migration
// 003 updates the row without touching revision, so while old and new
// binaries run side by side (a rolling deploy) a new writer can read
// revision n, have an old writer overwrite the row (still at n), and then
// commit its own write at n -- the old writer's change is lost and no
// ErrConflict is raised. Old writers never detect conflicts at all. Apply
// migration 003 first, then replace ALL old binaries (or stop them from
// writing) before relying on ErrConflict; until then concurrent updates
// can still be lost silently, exactly as before this check existed.
func (r *Repository) Update(ctx context.Context, db pgxtx.DBTX, c *workflow.Case, expectedLastSeq int64) error {
	tag, err := db.Exec(ctx, `
        UPDATE workflow_case SET
            definition = $2, version = $3, domain = $4, external_id = $5, unit = $6,
            state = $7, status = $8, assigned_to = $9, opened_at = $10, closed_at = $11, deadline_at = $12,
            last_seq = $14, tenant = $15, revision = revision + 1
        WHERE id = $1 AND revision = $13 AND last_seq = $16`,
		c.ID, c.Definition, c.Version, c.Domain, c.ExternalID, c.Unit,
		c.State, string(c.Status), c.AssignedTo, c.OpenedAt, c.ClosedAt, c.DeadlineAt,
		c.Revision, c.LastSeq, c.Tenant, expectedLastSeq,
	)
	if err != nil {
		return fmt.Errorf("workflow: update case: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workflow_case WHERE id = $1)`, c.ID).Scan(&exists); err != nil {
			return fmt.Errorf("workflow: update case: %w", err)
		}
		if !exists {
			return fmt.Errorf("workflow: update case: %w", workflow.ErrCaseNotFound)
		}
		return fmt.Errorf("workflow: update case %s at revision %d, last seq %d: %w", c.ID, c.Revision, expectedLastSeq, workflow.ErrConflict)
	}
	c.Revision++
	return nil
}

// AppendEvent inserts e with the Seq the caller assigned (the engine takes
// it from Case.LastSeq). A Seq below 1 is rejected. Seq is not recomputed
// here: a Seq that is already taken fails on UNIQUE (case_id, seq) instead
// of being silently moved, and is returned as workflow.ErrConflict (wrapping
// the Postgres error): a concurrent RecordFailure took it, or a writer that
// does not advance workflow_case.last_seq (a binary from before migration
// 004) did.
// An empty e.Detail is stored as `{}`, and a SchemaVersion below 1 (a caller
// that does not set it) as 1, the version of an event without detail.
// actor_id stores e.Actor.ID, or the deprecated e.ActorID when Actor.ID is
// empty; an empty e.Actor.Kind is stored as workflow.ActorUnspecified.
func (r *Repository) AppendEvent(ctx context.Context, db pgxtx.DBTX, e *workflow.Event) error {
	if e.Seq < 1 {
		return fmt.Errorf("workflow: append event: seq %d: must be at least 1", e.Seq)
	}
	schemaVersion := max(e.SchemaVersion, 1)
	detail := e.Detail
	if len(detail) == 0 {
		detail = json.RawMessage(`{}`)
	}
	actorID := e.Actor.ID
	if actorID == "" {
		actorID = e.ActorID //nolint:staticcheck // a caller that only sets the deprecated field still gets it stored
	}
	actorKind := e.Actor.Kind
	if actorKind == "" {
		actorKind = workflow.ActorUnspecified
	}
	_, err := db.Exec(ctx, `
        INSERT INTO workflow_event (`+eventColumns+`)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`,
		e.ID, e.CaseID, e.Seq, string(e.Kind), e.FromState, e.ToState, e.Action, actorID, e.OccurredAt, e.Comment,
		schemaVersion, detail, e.Tenant, string(actorKind), e.Actor.ActingAs, e.Actor.OnBehalfOf, e.Actor.Via,
		e.RequestID, e.TraceID, e.SpanID,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode && pgErr.ConstraintName == eventSeqConstraint {
			return fmt.Errorf("workflow: append event %s seq %d: %w: %w", e.CaseID, e.Seq, workflow.ErrConflict, err)
		}
		return fmt.Errorf("workflow: append event: %w", err)
	}
	return nil
}

// AdvanceLastSeq takes the next Seq of case id without changing anything
// else: it increments workflow_case.last_seq in one statement, which locks
// the row until the caller's transaction ends, and returns the case as
// updated (LastSeq is the Seq just taken). Revision is not advanced. A
// missing case fails with workflow.ErrCaseNotFound.
func (r *Repository) AdvanceLastSeq(ctx context.Context, db pgxtx.DBTX, id uuid.UUID) (*workflow.Case, error) {
	row := db.QueryRow(ctx, `
        UPDATE workflow_case SET last_seq = last_seq + 1
        WHERE id = $1
        RETURNING `+caseColumns, id)
	c, err := scanCase(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("workflow: advance last seq: %w", workflow.ErrCaseNotFound)
		}
		return nil, fmt.Errorf("workflow: advance last seq: %w", err)
	}
	return c, nil
}

// ListEvents returns every event for caseID, ordered by seq ascending, as
// stored: SchemaVersion is the row's schema_version (1 for rows written
// before migration 004, whose Actor.Kind reads as workflow.ActorUnspecified
// from the column default). Actor.ID and ActorID both hold actor_id.
// Upcasting older versions and rejecting newer ones is the engine's job (see
// workflow.Engine.History).
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

// ListByEligibility returns open cases matching f. An empty f.Tenant matches
// every tenant. States, when non-empty,
// restricts results to cases whose current node is one of those IDs — the
// mapping from Position/Unit to that set of node IDs is resolved by the
// caller (see workflow.Engine.EligibilityFor), not by this query.
func (r *Repository) ListByEligibility(ctx context.Context, db pgxtx.DBTX, f workflow.InboxFilter) ([]workflow.Case, error) {
	limit, offset, err := f.Page()
	if err != nil {
		return nil, fmt.Errorf("workflow: list by eligibility: %w", err)
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
          AND ($8::TEXT    IS NULL OR tenant = $8)
        ORDER BY opened_at ASC
        LIMIT $6 OFFSET $7`

	rows, err := db.Query(ctx, q,
		nullString(f.Domain),
		f.States,
		nullString(f.AssignedTo),
		f.Unassigned,
		f.Overdue,
		limit,
		offset,
		nullString(f.Tenant),
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
		&c.AssignedTo, &c.OpenedAt, &c.ClosedAt, &c.DeadlineAt, &c.Revision, &c.LastSeq, &c.Tenant,
	); err != nil {
		return nil, err
	}
	c.Status = workflow.Status(status)
	return &c, nil
}

func scanEvent(row rowScanner) (*workflow.Event, error) {
	var e workflow.Event
	var kind, actorKind string
	if err := row.Scan(
		&e.ID, &e.CaseID, &e.Seq, &kind, &e.FromState, &e.ToState, &e.Action, &e.Actor.ID, &e.OccurredAt, &e.Comment,
		&e.SchemaVersion, &e.Detail, &e.Tenant, &actorKind, &e.Actor.ActingAs, &e.Actor.OnBehalfOf, &e.Actor.Via,
		&e.RequestID, &e.TraceID, &e.SpanID,
	); err != nil {
		return nil, err
	}
	e.Kind = workflow.EventKind(kind)
	e.Actor.Kind = workflow.ActorKind(actorKind)
	e.ActorID = e.Actor.ID //nolint:staticcheck // keep the deprecated mirror populated for callers that still read it
	return &e, nil
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
