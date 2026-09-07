// Package audit defines the audit-trail port: the Entry record, the
// Auditable interface domain entities implement to be recorded automatically,
// and the Repository port a storage adapter implements (see audit/postgres).
//
// This package is the application layer for auditing. It reads the acting
// principal from vogel/auth and request-scoped metadata (request ID, IP,
// User-Agent) from vogel/reqctx — both dependency-light ports — and never
// imports httpx or a router package. That inversion is deliberate: the three
// systems this package was extracted from each had their audit recorder
// import the HTTP middleware package to read the current user and request
// info, coupling an application-layer concern to the transport layer and
// making this package impossible to reuse outside an HTTP handler (e.g. from
// a worker). See Recorder.Record and the Source type for how origin-specific
// recording (HTTP vs. worker) is handled instead.
package audit

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

// Action represents the type of mutation being audited.
type Action string

const (
	ActionCreate  Action = "create"
	ActionUpdate  Action = "update"
	ActionDelete  Action = "delete"
	ActionExecute Action = "execute"
)

// Status represents the outcome of an audited operation.
type Status string

const (
	StatusSuccess Status = "success"
	StatusFailed  Status = "failed"
	StatusPartial Status = "partial"
	StatusNoop    Status = "noop"
)

// Source identifies what kind of process produced an audit entry.
//
// It exists so an entry's origin is always stated explicitly rather than
// inferred: an HTTP-originated entry naturally carries a request ID (from
// reqctx, populated by httpx/middleware.RequestContext), while a
// worker-originated entry — a background job, a scheduled task, a queue
// consumer — legitimately has none, because there was never an inbound
// request to carry one. Recorder.Record takes a Source as a required
// argument (not an optional Option) precisely so a worker call site cannot
// silently inherit an HTTP-shaped default it never had — see the doc comment
// on Recorder.Record for the full rationale.
type Source string

const (
	// SourceHTTP marks an entry produced while handling an inbound HTTP
	// request.
	SourceHTTP Source = "http"

	// SourceWorker marks an entry produced by a background process with no
	// inbound HTTP request of its own — a queue consumer, a scheduled job,
	// or work spawned by (but running outside) a request. A SourceWorker
	// entry may still carry a non-empty RequestID if its context was derived
	// from the request that spawned it; that propagation is the caller's
	// responsibility, not this package's.
	SourceWorker Source = "worker"
)

// AffectedResource represents a secondary resource affected by a batch
// operation. See WithAffectedResources for the recommended way to audit a
// bulk operation as a single entry instead of one entry per affected row.
type AffectedResource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Auditable is implemented by domain entities that can be audited. The
// Recorder uses this to auto-generate snapshots, diffs, and human-readable
// labels — see WithSubject and WithChange.
type Auditable interface {
	// AuditRepr returns a human-readable label, e.g. "Invoice #45 - $1,200.00".
	AuditRepr() string
	// AuditSnapshot returns the full serializable state of the entity.
	AuditSnapshot() map[string]any
}

// Entry represents a single audit log record.
type Entry struct {
	ID uuid.UUID

	// Resource identification.
	ResourceType  string
	ResourceID    string
	AggregateType *string
	AggregateID   *string

	// Operation semantics.
	OperationCategory string // "create", "update", "delete", "execute"
	OperationName     string // e.g. "create_invoice", "add_line_items"
	Status            string // "success", "failed", "partial", "noop"

	// Actor & origin.
	ActorID   string
	Username  string
	Source    string
	RequestID *string
	IP        string
	UserAgent string

	// Human-readable label.
	ObjectRepr string

	// Payloads & snapshots.
	InputPayload      map[string]any
	BeforeSnapshot    map[string]any
	AfterSnapshot     map[string]any
	ChangedFields     map[string]any
	AffectedResources []AffectedResource

	// Error tracking.
	ErrorCode    *string
	ErrorMessage *string

	CreatedAt time.Time
}

// Filters defines the criteria for listing audit entries.
type Filters struct {
	ActorID      string
	ResourceType string
	// ResourceID, when set, restricts results to entries whose
	// audit_log.resource_id matches this UUID. nil means no filter. Typed as
	// *uuid.UUID (not string) so an invalid ID is rejected by the caller's
	// own parsing before it ever reaches a query.
	ResourceID        *uuid.UUID
	OperationCategory string
	OperationName     string
	Status            string
	AggregateType     string
	AggregateID       string
	From              *time.Time
	To                *time.Time
	Limit             int
	Offset            int
}

// Repository defines persistence operations for audit entries. See
// audit/postgres for the PostgreSQL-backed implementation.
type Repository interface {
	// Create inserts an entry using db, which may be a plain pool connection
	// or a live transaction (see vogel/pgxtx.DBTX) — allowing a caller to
	// record an audit entry in the same transaction as the mutation it
	// describes.
	Create(ctx context.Context, db pgxtx.DBTX, entry *Entry) error
	GetByID(ctx context.Context, id uuid.UUID) (*Entry, error)
	List(ctx context.Context, f Filters) ([]Entry, int, error)
}
