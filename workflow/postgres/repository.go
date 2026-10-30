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

// eventWriteColumns adds the token and scope columns of migration 008 to the
// columns every event has.
const eventWriteColumns = eventColumns + `, token_id, scope_id`

// eventReadColumns reads a NULL token_id or scope_id (an event stored before
// migration 008) as the case id — the root token in the root scope — and
// adds the hash chain columns, which migration 007's trigger sets on insert.
const eventReadColumns = eventColumns + `, COALESCE(token_id, case_id), COALESCE(scope_id, case_id), prev_digest, digest`

const scopeColumns = `id, case_id, parent_id, split_node, join_node, expected, status, opened_seq, closed_seq`

const tokenColumns = `id, case_id, scope_id, node, position, unit, status, assigned_to, deadline_at,
    entered_seq, entered_at, ended_seq, ended_at`

// uniqueViolationCode is the PostgreSQL error code for a unique constraint
// violation (23505).
const uniqueViolationCode = "23505"

// eventSeqConstraint is the name Postgres gave UNIQUE (case_id, seq) on
// workflow_event in migration 001. A violation of it means another writer
// appended that Seq first.
const eventSeqConstraint = "workflow_event_case_id_seq_key"

// joinedOnceIndex is migration 008's unique index allowing one "joined"
// event per scope. A violation of it means the scope already joined.
const joinedOnceIndex = "uq_workflow_event_joined_once"

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
// e.PrevDigest and e.Digest are not written: migration 007's trigger computes
// them from the stored row, and AppendEvent sets them on e from the insert.
// e.TokenID and e.ScopeID are stored as given, uuid.Nil as NULL. A second
// "joined" event for one scope fails on migration 008's
// uq_workflow_event_joined_once and is returned as workflow.ErrConflict too.
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
	err := db.QueryRow(ctx, `
        INSERT INTO workflow_event (`+eventWriteColumns+`)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
        RETURNING prev_digest, digest`,
		e.ID, e.CaseID, e.Seq, string(e.Kind), e.FromState, e.ToState, e.Action, actorID, e.OccurredAt, e.Comment,
		schemaVersion, detail, e.Tenant, string(actorKind), e.Actor.ActingAs, e.Actor.OnBehalfOf, e.Actor.Via,
		e.RequestID, e.TraceID, e.SpanID, nullUUID(e.TokenID), nullUUID(e.ScopeID),
	).Scan(&e.PrevDigest, &e.Digest)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode &&
			(pgErr.ConstraintName == eventSeqConstraint || pgErr.ConstraintName == joinedOnceIndex) {
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
// from the column default). Actor.ID and ActorID both hold actor_id. TokenID
// and ScopeID read as the case id for rows stored before migration 008.
// Upcasting older versions and rejecting newer ones is the engine's job (see
// workflow.Engine.History).
func (r *Repository) ListEvents(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]workflow.Event, error) {
	rows, err := db.Query(ctx, `SELECT `+eventReadColumns+` FROM workflow_event WHERE case_id = $1 ORDER BY seq ASC`, caseID)
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

// ListEventDigests returns caseID's hash chain links in seq order: each
// row's stored prev_digest and digest, and the digest migration 007's
// vogel_workflow_event_digest computes again from the row as it is now and
// its stored prev_digest (nil for an unchained row).
func (r *Repository) ListEventDigests(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]workflow.EventDigest, error) {
	rows, err := db.Query(ctx, `
        SELECT e.seq, e.prev_digest, e.digest,
               CASE WHEN e.digest IS NULL THEN NULL ELSE vogel_workflow_event_digest(e.prev_digest, e) END
        FROM workflow_event e
        WHERE e.case_id = $1
        ORDER BY e.seq ASC`, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: list event digests: %w", err)
	}
	defer rows.Close()

	var links []workflow.EventDigest
	for rows.Next() {
		var l workflow.EventDigest
		if err := rows.Scan(&l.Seq, &l.PrevDigest, &l.Digest, &l.Recomputed); err != nil {
			return nil, fmt.Errorf("workflow: list event digests: %w", err)
		}
		links = append(links, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workflow: list event digests: %w", err)
	}
	return links, nil
}

// ListWorkItems returns one workflow.WorkItem per active token of an open
// case matching f, oldest case first (opened_at, as before tokens existed,
// then case id), then the case's tokens by entered_seq and id, paged by
// f.Page.
// An empty f.Tenant matches every tenant. f.States restricts the token's
// node, f.Eligibility its stored (position, unit) pair, and f.AssignedTo,
// f.Unassigned and f.Overdue its assignment and deadline. Turning a person's
// positions into f.Eligibility is the caller's job (see
// workflow.Engine.EligibilityForToken), not this query's.
func (r *Repository) ListWorkItems(ctx context.Context, db pgxtx.DBTX, f workflow.InboxFilter) ([]workflow.WorkItem, error) {
	limit, offset, err := f.Page()
	if err != nil {
		return nil, fmt.Errorf("workflow: list work items: %w", err)
	}
	var positions, units []string
	for _, e := range f.Eligibility {
		positions = append(positions, e.Position)
		units = append(units, e.Unit)
	}

	const q = `
        SELECT ` + prefixedCaseColumns + `, ` + prefixedTokenColumns + `
        FROM workflow_token t
        JOIN workflow_case c ON c.id = t.case_id
        WHERE t.status = 'active' AND c.status = 'open'
          AND ($1::TEXT    IS NULL OR c.domain = $1)
          AND ($2::TEXT[]  IS NULL OR t.node = ANY($2))
          AND ($3::TEXT    IS NULL OR t.assigned_to = $3)
          AND (NOT $4::BOOLEAN OR t.assigned_to = '')
          AND (NOT $5::BOOLEAN OR (t.deadline_at IS NOT NULL AND t.deadline_at < now()))
          AND ($8::TEXT    IS NULL OR c.tenant = $8)
          AND ($9::TEXT[]  IS NULL OR (t.position, t.unit) IN (SELECT * FROM unnest($9::TEXT[], $10::TEXT[])))
        ORDER BY c.opened_at ASC, c.id, t.entered_seq ASC, t.id
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
		positions,
		units,
	)
	if err != nil {
		return nil, fmt.Errorf("workflow: list work items: %w", err)
	}
	defer rows.Close()

	var items []workflow.WorkItem
	for rows.Next() {
		var it workflow.WorkItem
		var status, tokenStatus string
		if err := rows.Scan(append(caseDest(&it.Case, &status), tokenDest(&it.Token, &tokenStatus)...)...); err != nil {
			return nil, fmt.Errorf("workflow: list work items: %w", err)
		}
		it.Case.Status = workflow.Status(status)
		it.Token.Status = workflow.TokenStatus(tokenStatus)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workflow: list work items: %w", err)
	}
	return items, nil
}

// CreateScope inserts s.
func (r *Repository) CreateScope(ctx context.Context, db pgxtx.DBTX, s *workflow.Scope) error {
	if _, err := db.Exec(ctx, `INSERT INTO workflow_scope (`+scopeColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		s.ID, s.CaseID, s.ParentID, s.SplitNode, s.JoinNode, s.Expected, string(s.Status), s.OpenedSeq, nullSeq(s.ClosedSeq),
	); err != nil {
		return fmt.Errorf("workflow: create scope: %w", err)
	}
	return nil
}

// UpdateScope writes s's status and closed seq, the only fields that change
// after a scope opens. A missing scope fails with workflow.ErrConflict.
func (r *Repository) UpdateScope(ctx context.Context, db pgxtx.DBTX, s *workflow.Scope) error {
	tag, err := db.Exec(ctx, `UPDATE workflow_scope SET status = $2, closed_seq = $3 WHERE id = $1`,
		s.ID, string(s.Status), nullSeq(s.ClosedSeq))
	if err != nil {
		return fmt.Errorf("workflow: update scope: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("workflow: update scope %s: %w", s.ID, workflow.ErrConflict)
	}
	return nil
}

// ListScopes returns every scope of caseID, in the order they opened.
func (r *Repository) ListScopes(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]workflow.Scope, error) {
	rows, err := db.Query(ctx, `SELECT `+scopeColumns+` FROM workflow_scope WHERE case_id = $1 ORDER BY opened_seq, id`, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: list scopes: %w", err)
	}
	defer rows.Close()

	var scopes []workflow.Scope
	for rows.Next() {
		var s workflow.Scope
		var status string
		var closed *int64
		if err := rows.Scan(&s.ID, &s.CaseID, &s.ParentID, &s.SplitNode, &s.JoinNode, &s.Expected, &status, &s.OpenedSeq, &closed); err != nil {
			return nil, fmt.Errorf("workflow: list scopes: %w", err)
		}
		s.Status = workflow.ScopeStatus(status)
		if closed != nil {
			s.ClosedSeq = *closed
		}
		scopes = append(scopes, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workflow: list scopes: %w", err)
	}
	return scopes, nil
}

// CreateToken inserts t.
func (r *Repository) CreateToken(ctx context.Context, db pgxtx.DBTX, t *workflow.Token) error {
	if _, err := db.Exec(ctx, `
        INSERT INTO workflow_token (`+tokenColumns+`)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		t.ID, t.CaseID, t.ScopeID, t.Node, t.Eligible.Position, t.Eligible.Unit, string(t.Status), t.AssignedTo, t.DeadlineAt,
		t.EnteredSeq, t.EnteredAt, nullSeq(t.EndedSeq), t.EndedAt,
	); err != nil {
		return fmt.Errorf("workflow: create token: %w", err)
	}
	return nil
}

// UpdateToken writes every mutable field of t. A missing token fails with
// workflow.ErrConflict.
func (r *Repository) UpdateToken(ctx context.Context, db pgxtx.DBTX, t *workflow.Token) error {
	tag, err := db.Exec(ctx, `
        UPDATE workflow_token SET
            node = $2, position = $3, unit = $4, status = $5, assigned_to = $6, deadline_at = $7,
            entered_seq = $8, entered_at = $9, ended_seq = $10, ended_at = $11
        WHERE id = $1`,
		t.ID, t.Node, t.Eligible.Position, t.Eligible.Unit, string(t.Status), t.AssignedTo, t.DeadlineAt,
		t.EnteredSeq, t.EnteredAt, nullSeq(t.EndedSeq), t.EndedAt,
	)
	if err != nil {
		return fmt.Errorf("workflow: update token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("workflow: update token %s: %w", t.ID, workflow.ErrConflict)
	}
	return nil
}

// ListTokens returns every token of caseID, ordered by entered_seq, then id.
func (r *Repository) ListTokens(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]workflow.Token, error) {
	rows, err := db.Query(ctx, `SELECT `+tokenColumns+` FROM workflow_token WHERE case_id = $1 ORDER BY entered_seq, id`, caseID)
	if err != nil {
		return nil, fmt.Errorf("workflow: list tokens: %w", err)
	}
	defer rows.Close()

	var tokens []workflow.Token
	for rows.Next() {
		var t workflow.Token
		var status string
		if err := rows.Scan(tokenDest(&t, &status)...); err != nil {
			return nil, fmt.Errorf("workflow: list tokens: %w", err)
		}
		t.Status = workflow.TokenStatus(status)
		tokens = append(tokens, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workflow: list tokens: %w", err)
	}
	return tokens, nil
}

// prefixedCaseColumns and prefixedTokenColumns are caseColumns and
// tokenColumns qualified by the aliases ListWorkItems uses.
const prefixedCaseColumns = `c.id, c.definition, c.version, c.domain, c.external_id, c.unit, c.state, c.status,
    c.assigned_to, c.opened_at, c.closed_at, c.deadline_at, c.revision, c.last_seq, c.tenant`

const prefixedTokenColumns = `t.id, t.case_id, t.scope_id, t.node, t.position, t.unit, t.status, t.assigned_to, t.deadline_at,
    t.entered_seq, t.entered_at, t.ended_seq, t.ended_at`

// caseDest returns the scan destinations of caseColumns into c, the status
// into status.
func caseDest(c *workflow.Case, status *string) []any {
	return []any{
		&c.ID, &c.Definition, &c.Version, &c.Domain, &c.ExternalID, &c.Unit, &c.State, status,
		&c.AssignedTo, &c.OpenedAt, &c.ClosedAt, &c.DeadlineAt, &c.Revision, &c.LastSeq, &c.Tenant,
	}
}

// tokenDest returns the scan destinations of tokenColumns into t, the status
// into status; a NULL ended_seq reads as 0.
func tokenDest(t *workflow.Token, status *string) []any {
	return []any{
		&t.ID, &t.CaseID, &t.ScopeID, &t.Node, &t.Eligible.Position, &t.Eligible.Unit, status, &t.AssignedTo, &t.DeadlineAt,
		&t.EnteredSeq, &t.EnteredAt, (*nullableSeq)(&t.EndedSeq), &t.EndedAt,
	}
}

// ListParticipated returns the distinct cases in which f.ActorID is the
// actor of any event, ordered by that actor's newest event in each case
// (newest first, case id as the tie-break), restricted to f.Tenant when set
// and paged by f.Page. It reads the actor's events through
// idx_workflow_event_actor (migration 006), filtering the tenant on the
// events themselves (each event copies its case's tenant) before grouping.
// The cost grows with how many events the actor wrote in the tenant, not
// with the page size.
func (r *Repository) ListParticipated(ctx context.Context, db pgxtx.DBTX, f workflow.ParticipationFilter) ([]workflow.Case, error) {
	limit, offset, err := f.Page()
	if err != nil {
		return nil, fmt.Errorf("workflow: list participated: %w", err)
	}

	const q = `
        SELECT ` + caseColumns + `
        FROM (
            SELECT case_id, MAX(occurred_at) AS last_at
            FROM workflow_event
            WHERE actor_id = $1 AND actor_id <> ''
              AND ($2::TEXT IS NULL OR tenant = $2)
            GROUP BY case_id
        ) p
        JOIN workflow_case ON workflow_case.id = p.case_id
        ORDER BY p.last_at DESC, workflow_case.id
        LIMIT $3 OFFSET $4`

	rows, err := db.Query(ctx, q, f.ActorID, nullString(f.Tenant), limit, offset)
	if err != nil {
		return nil, fmt.Errorf("workflow: list participated: %w", err)
	}
	defer rows.Close()

	var cases []workflow.Case
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, fmt.Errorf("workflow: list participated: %w", err)
		}
		cases = append(cases, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workflow: list participated: %w", err)
	}
	return cases, nil
}

func scanCase(row rowScanner) (*workflow.Case, error) {
	var c workflow.Case
	var status string
	if err := row.Scan(caseDest(&c, &status)...); err != nil {
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
		&e.RequestID, &e.TraceID, &e.SpanID, &e.TokenID, &e.ScopeID, &e.PrevDigest, &e.Digest,
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

// nullUUID stores uuid.Nil as NULL.
func nullUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// nullSeq stores a zero seq (not set yet) as NULL.
func nullSeq(seq int64) *int64 {
	if seq == 0 {
		return nil
	}
	return &seq
}

// nullableSeq scans a nullable BIGINT seq column, NULL as 0.
type nullableSeq int64

func (n *nullableSeq) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*n = 0
	case int64:
		*n = nullableSeq(v)
	default:
		return fmt.Errorf("workflow: scan seq: unexpected %T", src)
	}
	return nil
}
