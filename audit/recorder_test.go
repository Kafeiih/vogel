package audit

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/reqctx"
)

// fakeRepository is an in-memory Repository used to test Recorder without a
// database.
type fakeRepository struct {
	created []*Entry
	err     error
}

func (f *fakeRepository) Create(_ context.Context, _ pgxtx.DBTX, entry *Entry) error {
	if f.err != nil {
		return f.err
	}
	f.created = append(f.created, entry)
	return nil
}

func (f *fakeRepository) GetByID(_ context.Context, _ uuid.UUID) (*Entry, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeRepository) List(_ context.Context, _ Filters) ([]Entry, int, error) {
	return nil, 0, errors.New("not implemented")
}

type fakeAuditable struct {
	repr     string
	snapshot map[string]any
}

func (f fakeAuditable) AuditRepr() string             { return f.repr }
func (f fakeAuditable) AuditSnapshot() map[string]any { return f.snapshot }

// TestRecord_RequestIDInContext_IsPersistedOnEntry is the regression test for
// FIX 1: the headline defect is that audit_log.request_id is declared,
// mapped, and persisted, but never assigned anywhere. This proves the
// Recorder actually stamps it from reqctx.
func TestRecord_RequestIDInContext_IsPersistedOnEntry(t *testing.T) {
	repo := &fakeRepository{}
	r := NewRecorder(repo)

	ctx := reqctx.WithRequestID(context.Background(), "req-abc-123")

	err := r.Record(ctx, nil, SourceHTTP, ActionCreate, "widget", "w-1")
	if err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	if len(repo.created) != 1 {
		t.Fatalf("expected 1 entry created, got %d", len(repo.created))
	}
	entry := repo.created[0]
	if entry.RequestID == nil {
		t.Fatal("expected RequestID to be set, got nil")
	}
	if *entry.RequestID != "req-abc-123" {
		t.Errorf("RequestID = %q, want %q", *entry.RequestID, "req-abc-123")
	}
}

// TestRecord_NoRequestIDInContext_LeavesRequestIDNilWithoutError proves the
// second half of FIX 1: a caller with no request in flight (e.g. a worker,
// or a test) still gets a valid entry rather than an error — RequestID is
// legitimately absent, not a fault.
func TestRecord_NoRequestIDInContext_LeavesRequestIDNilWithoutError(t *testing.T) {
	repo := &fakeRepository{}
	r := NewRecorder(repo)

	err := r.Record(context.Background(), nil, SourceWorker, ActionExecute, "job", "j-1")
	if err != nil {
		t.Fatalf("Record returned error for a context with no request ID: %v", err)
	}

	if len(repo.created) != 1 {
		t.Fatalf("expected 1 entry created, got %d", len(repo.created))
	}
	if repo.created[0].RequestID != nil {
		t.Errorf("RequestID = %v, want nil", *repo.created[0].RequestID)
	}
}

// TestRecord_ActorFromAuthContext_IsPersisted proves the actor and the
// request metadata are read from the same unconditional step: setting only
// the principal (no reqctx values at all) must not prevent the actor from
// landing on the entry.
func TestRecord_ActorFromAuthContext_IsPersisted(t *testing.T) {
	repo := &fakeRepository{}
	r := NewRecorder(repo)

	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		UserID:   "u-1",
		Username: "alice",
	})

	err := r.Record(ctx, nil, SourceHTTP, ActionUpdate, "widget", "w-1")
	if err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	entry := repo.created[0]
	if entry.ActorID != "u-1" || entry.Username != "alice" {
		t.Errorf("actor = %q/%q, want %q/%q", entry.ActorID, entry.Username, "u-1", "alice")
	}
}

// TestRecord_ActorAndRequestMetadata_BothPopulatedTogether proves FIX 1's
// core design constraint: the actor and the request ID/IP/User-Agent arrive
// from one call, so there is no code path that sets one without the other.
func TestRecord_ActorAndRequestMetadata_BothPopulatedTogether(t *testing.T) {
	repo := &fakeRepository{}
	r := NewRecorder(repo)

	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{UserID: "u-1", Username: "alice"})
	ctx = reqctx.WithRequestID(ctx, "req-1")
	ctx = reqctx.WithRequestInfo(ctx, reqctx.RequestInfo{IP: "10.0.0.1", UserAgent: "curl/8"})

	if err := r.Record(ctx, nil, SourceHTTP, ActionCreate, "widget", "w-1"); err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	entry := repo.created[0]
	if entry.ActorID != "u-1" {
		t.Errorf("ActorID = %q, want %q", entry.ActorID, "u-1")
	}
	if entry.RequestID == nil || *entry.RequestID != "req-1" {
		t.Errorf("RequestID = %v, want %q", entry.RequestID, "req-1")
	}
	if entry.IP != "10.0.0.1" || entry.UserAgent != "curl/8" {
		t.Errorf("IP/UserAgent = %q/%q, want %q/%q", entry.IP, entry.UserAgent, "10.0.0.1", "curl/8")
	}
}

// TestRecord_SourceHTTP_And_SourceWorker is the regression test for FIX 5:
// the origin must be recorded exactly as given, never silently defaulted.
func TestRecord_SourceHTTP_And_SourceWorker(t *testing.T) {
	tests := []struct {
		name   string
		source Source
	}{
		{"http", SourceHTTP},
		{"worker", SourceWorker},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepository{}
			r := NewRecorder(repo)

			if err := r.Record(context.Background(), nil, tt.source, ActionExecute, "job", "j-1"); err != nil {
				t.Fatalf("Record returned error: %v", err)
			}

			if got := repo.created[0].Source; got != string(tt.source) {
				t.Errorf("Source = %q, want %q", got, tt.source)
			}
		})
	}
}

// TestRecord_WorkerOrigin_RequestIDStillPropagatedWhenPresent proves that
// SourceWorker does not force RequestID to nil: a background job whose
// context was derived from the request that spawned it should still carry
// that request's ID, so the two can be linked.
func TestRecord_WorkerOrigin_RequestIDStillPropagatedWhenPresent(t *testing.T) {
	repo := &fakeRepository{}
	r := NewRecorder(repo)

	ctx := reqctx.WithRequestID(context.Background(), "req-parent")

	if err := r.Record(ctx, nil, SourceWorker, ActionExecute, "job", "j-1"); err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	entry := repo.created[0]
	if entry.Source != string(SourceWorker) {
		t.Errorf("Source = %q, want %q", entry.Source, SourceWorker)
	}
	if entry.RequestID == nil || *entry.RequestID != "req-parent" {
		t.Errorf("RequestID = %v, want %q", entry.RequestID, "req-parent")
	}
}

func TestWithSubject_Create_SetsAfterSnapshotAndChangedFields(t *testing.T) {
	repo := &fakeRepository{}
	r := NewRecorder(repo)

	subject := fakeAuditable{repr: "Widget #1", snapshot: map[string]any{"name": "gizmo"}}

	if err := r.Record(context.Background(), nil, SourceHTTP, ActionCreate, "widget", "w-1", WithSubject(subject)); err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	entry := repo.created[0]
	if entry.ObjectRepr != "Widget #1" {
		t.Errorf("ObjectRepr = %q, want %q", entry.ObjectRepr, "Widget #1")
	}
	if entry.AfterSnapshot["name"] != "gizmo" {
		t.Errorf("AfterSnapshot[name] = %v, want %q", entry.AfterSnapshot["name"], "gizmo")
	}
	change, ok := entry.ChangedFields["name"].(map[string]any)
	if !ok {
		t.Fatalf("ChangedFields[name] not a map: %#v", entry.ChangedFields["name"])
	}
	if change["old"] != nil || change["new"] != "gizmo" {
		t.Errorf("ChangedFields[name] = %#v, want old=nil new=gizmo", change)
	}
}

func TestWithChange_Update_ComputesDiff(t *testing.T) {
	repo := &fakeRepository{}
	r := NewRecorder(repo)

	before := fakeAuditable{repr: "Widget #1", snapshot: map[string]any{"name": "gizmo", "qty": 1}}
	after := fakeAuditable{repr: "Widget #1", snapshot: map[string]any{"name": "gizmo", "qty": 2}}

	if err := r.Record(context.Background(), nil, SourceHTTP, ActionUpdate, "widget", "w-1", WithChange(before, after)); err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	entry := repo.created[0]
	if _, changed := entry.ChangedFields["qty"]; !changed {
		t.Error("expected qty to be reported as changed")
	}
	if _, changed := entry.ChangedFields["name"]; changed {
		t.Error("expected name to not be reported as changed (unchanged value)")
	}
}

func TestWithAffectedResources_SetsListOnSingleEntry(t *testing.T) {
	repo := &fakeRepository{}
	r := NewRecorder(repo)

	resources := []AffectedResource{{Type: "cobro", ID: "c-1"}, {Type: "cobro", ID: "c-2"}}

	if err := r.Record(context.Background(), nil, SourceHTTP, ActionCreate, "derecho", "d-1", WithAffectedResources(resources)); err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	// Exactly one entry, naming both affected resources — not one entry per resource.
	if len(repo.created) != 1 {
		t.Fatalf("expected exactly 1 entry for a batched operation, got %d", len(repo.created))
	}
	if len(repo.created[0].AffectedResources) != 2 {
		t.Fatalf("expected 2 affected resources, got %d", len(repo.created[0].AffectedResources))
	}
}

func TestWithError_SetsFailedStatusAndDetails(t *testing.T) {
	repo := &fakeRepository{}
	r := NewRecorder(repo)

	err := r.Record(context.Background(), nil, SourceHTTP, ActionUpdate, "widget", "w-1",
		WithError("VALIDATION_FAILED", "qty must be positive"))
	if err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	entry := repo.created[0]
	if entry.Status != string(StatusFailed) {
		t.Errorf("Status = %q, want %q", entry.Status, StatusFailed)
	}
	if entry.ErrorCode == nil || *entry.ErrorCode != "VALIDATION_FAILED" {
		t.Errorf("ErrorCode = %v, want %q", entry.ErrorCode, "VALIDATION_FAILED")
	}
}

func TestRecord_RepositoryError_Propagates(t *testing.T) {
	wantErr := errors.New("db unavailable")
	repo := &fakeRepository{err: wantErr}
	r := NewRecorder(repo)

	err := r.Record(context.Background(), nil, SourceHTTP, ActionCreate, "widget", "w-1")
	if !errors.Is(err, wantErr) {
		t.Errorf("Record error = %v, want %v", err, wantErr)
	}
}
