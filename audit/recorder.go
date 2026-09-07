package audit

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/reqctx"
)

// Option is a functional option for customizing an audit entry.
type Option func(*Entry)

// WithSubject auto-generates ObjectRepr and snapshot from an Auditable entity.
//   - ActionCreate → AfterSnapshot = snapshot
//   - ActionDelete → BeforeSnapshot = snapshot
//
// For UPDATE use WithChange instead.
func WithSubject(a Auditable) Option {
	return func(e *Entry) {
		e.ObjectRepr = a.AuditRepr()
		snapshot := a.AuditSnapshot()

		switch Action(e.OperationCategory) {
		case ActionCreate:
			e.AfterSnapshot = snapshot
			e.ChangedFields = changesFromCreate(snapshot)
		case ActionDelete:
			e.BeforeSnapshot = snapshot
			e.ChangedFields = changesFromDelete(snapshot)
		case ActionUpdate, ActionExecute:
			// Update uses WithChange (before/after diff). Execute has no snapshot to infer.
		}
	}
}

// WithChange auto-generates ObjectRepr, before/after snapshots, and
// ChangedFields by diffing the two states. Use this for UPDATE actions.
func WithChange(before, after Auditable) Option {
	return func(e *Entry) {
		e.ObjectRepr = after.AuditRepr()

		beforeSnap := before.AuditSnapshot()
		afterSnap := after.AuditSnapshot()

		e.BeforeSnapshot = beforeSnap
		e.AfterSnapshot = afterSnap
		e.ChangedFields = computeChanges(beforeSnap, afterSnap)
	}
}

// WithObjectRepr manually sets the human-readable label.
func WithObjectRepr(repr string) Option {
	return func(e *Entry) {
		e.ObjectRepr = repr
	}
}

// WithInputPayload attaches the raw request payload to the audit entry.
func WithInputPayload(d map[string]any) Option {
	return func(e *Entry) {
		e.InputPayload = d
	}
}

// WithChangedFields manually sets field-level change data. This overrides
// any auto-computed diff from WithChange.
func WithChangedFields(c map[string]any) Option {
	return func(e *Entry) {
		e.ChangedFields = c
	}
}

// WithBeforeSnapshot manually sets the before-state snapshot.
func WithBeforeSnapshot(data map[string]any) Option {
	return func(e *Entry) {
		e.BeforeSnapshot = data
	}
}

// WithAfterSnapshot manually sets the after-state snapshot.
func WithAfterSnapshot(data map[string]any) Option {
	return func(e *Entry) {
		e.AfterSnapshot = data
	}
}

// WithAggregate sets the parent aggregate context for child-resource operations.
func WithAggregate(aggregateType, aggregateID string) Option {
	return func(e *Entry) {
		e.AggregateType = &aggregateType
		e.AggregateID = &aggregateID
	}
}

// WithOperationName overrides the auto-derived operation name.
func WithOperationName(name string) Option {
	return func(e *Entry) {
		e.OperationName = name
	}
}

// WithStatus overrides the default "success" status.
func WithStatus(status Status) Option {
	return func(e *Entry) {
		e.Status = string(status)
	}
}

// WithAffectedResources sets the list of secondary resources touched by a
// single logical operation, as one entry naming all of them.
//
// This is the intended way to audit a bulk/batch operation: record one entry
// on the primary resource with WithAffectedResources listing every row it
// touched, rather than looping and calling Record once per row. The latter
// multiplies INSERTs inside whatever transaction the caller is holding,
// extending lock hold time in proportion to batch size for no benefit — the
// audit trail does not need one row per affected resource to remain
// queryable, since AffectedResources is itself a queryable JSON column.
func WithAffectedResources(resources []AffectedResource) Option {
	return func(e *Entry) {
		e.AffectedResources = resources
	}
}

// WithError sets the status to "failed" and records error details.
func WithError(code, message string) Option {
	return func(e *Entry) {
		e.Status = string(StatusFailed)
		e.ErrorCode = &code
		e.ErrorMessage = &message
	}
}

// Recorder provides a simple API for recording audit entries.
type Recorder struct {
	repo Repository
}

// NewRecorder creates a new Recorder with the given repository.
func NewRecorder(repo Repository) *Recorder {
	return &Recorder{repo: repo}
}

// Record creates an audit entry using the given db (a pool or a live
// transaction — see vogel/pgxtx.DBTX).
//
// source is a required argument, not an Option, and this is deliberate
// (FIX 5): the three systems this package was extracted from defaulted every
// entry's Source to "http" inside the struct literal, so a background job
// that called Record — there are none as of this writing, precisely because
// there was no way to mark one correctly — would have silently produced an
// HTTP-shaped audit row for work that never touched a request. Requiring the
// caller to state SourceHTTP or SourceWorker up front makes that omission a
// compile error instead of a silent data-quality bug.
//
// The acting principal and the request's metadata are read from context in
// this same call, unconditionally — not behind a separate Option a caller
// could add the resource/subject options for but forget the context ones
// for. This is the fix for FIX 1: previously the recorder read the actor and
// the request's IP/User-Agent from context but never read a request ID at
// all (there was nowhere dependency-inversion-clean to read one from), so
// every audit_log.request_id in production is NULL. Now that reqctx is the
// single owner of the request ID (see vogel/reqctx and
// httpx/middleware.RequestContext), Record reads it the same way it reads
// the actor: as part of the one unconditional step below, so a future
// maintainer cannot add a new call site that "forgets" the request ID
// without also forgetting the actor — both come from the same call.
//
// Record also never imports httpx or a router package (FIX 2): it reads the
// actor via vogel/auth.FromContext and the request metadata via
// vogel/reqctx, both dependency-light application-facing ports, instead of
// the HTTP transport-layer middleware.UserFromContext /
// middleware.RequestInfoFromContext the source recorder called. That
// inversion is what makes Recorder usable from a worker at all — the
// worker's context has no *http.Request, but it can still be constructed
// with an auth.Principal (or none) and (optionally) a propagated reqctx
// request ID.
//
// Transactional flow (recommended for commands — audit lives in the same TX):
//
//	txManager.WithTx(ctx, func(txCtx context.Context) error {
//	    repo.CreateTx(txCtx, entity)
//	    return recorder.Record(txCtx, db, audit.SourceHTTP, audit.ActionCreate, "item", id,
//	        audit.WithSubject(entity),
//	    )
//	})
func (r *Recorder) Record(ctx context.Context, db pgxtx.DBTX, source Source, action Action, resourceType, resourceID string, opts ...Option) error {
	category := strings.ToLower(string(action))

	entry := &Entry{
		ID:                uuid.New(),
		ResourceType:      resourceType,
		ResourceID:        resourceID,
		OperationCategory: category,
		OperationName:     category + "_" + resourceType,
		Status:            string(StatusSuccess),
		Source:            string(source),
		CreatedAt:         time.Now(),
	}

	applyActorAndRequestContext(ctx, entry)

	for _, opt := range opts {
		opt(entry)
	}

	return r.repo.Create(ctx, db, entry)
}

// applyActorAndRequestContext stamps e with the acting principal and the
// request-scoped metadata found in ctx, in one unconditional step — see the
// doc comment on Recorder.Record for why this must not be split into
// separately-skippable pieces.
func applyActorAndRequestContext(ctx context.Context, e *Entry) {
	if p := auth.FromContext(ctx); p != nil {
		e.ActorID = p.UserID
		e.Username = p.Username
	}

	if id := reqctx.RequestIDFromContext(ctx); id != "" {
		e.RequestID = &id
	}

	if info, ok := reqctx.RequestInfoFromContext(ctx); ok {
		e.IP = info.IP
		e.UserAgent = info.UserAgent
	}
}

// changesFromCreate builds ChangedFields for a CREATE: every field goes from nil → value.
func changesFromCreate(snapshot map[string]any) map[string]any {
	changes := make(map[string]any, len(snapshot))
	for k, v := range snapshot {
		changes[k] = map[string]any{"old": nil, "new": v}
	}
	return changes
}

// changesFromDelete builds ChangedFields for a DELETE: every field goes from value → nil.
func changesFromDelete(snapshot map[string]any) map[string]any {
	changes := make(map[string]any, len(snapshot))
	for k, v := range snapshot {
		changes[k] = map[string]any{"old": v, "new": nil}
	}
	return changes
}

// computeChanges diffs two snapshots and returns a map of changed fields
// with {"old": oldVal, "new": newVal} for each difference.
func computeChanges(before, after map[string]any) map[string]any {
	changes := make(map[string]any)

	for k, newVal := range after {
		oldVal, exists := before[k]
		if !exists || !reflect.DeepEqual(oldVal, newVal) {
			changes[k] = map[string]any{"old": oldVal, "new": newVal}
		}
	}

	for k, oldVal := range before {
		if _, exists := after[k]; !exists {
			changes[k] = map[string]any{"old": oldVal, "new": nil}
		}
	}

	return changes
}
