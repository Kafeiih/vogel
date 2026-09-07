package httpx

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/audit"
)

func strPtr(s string) *string { return &s }

func TestToResponse_MapsEveryField(t *testing.T) {
	id := uuid.New()
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	entry := &audit.Entry{
		ID:                id,
		ResourceType:      "invoice",
		ResourceID:        "inv-1",
		AggregateType:     strPtr("order"),
		AggregateID:       strPtr("ord-1"),
		OperationCategory: "create",
		OperationName:     "create_invoice",
		Status:            "success",
		ActorID:           "actor-1",
		Username:          "alice",
		Source:            "http",
		RequestID:         strPtr("req-1"),
		IP:                "10.0.0.1",
		UserAgent:         "test-agent",
		ObjectRepr:        "Invoice #1",
		InputPayload:      map[string]any{"a": 1},
		BeforeSnapshot:    map[string]any{"b": 2},
		AfterSnapshot:     map[string]any{"c": 3},
		ChangedFields:     map[string]any{"d": 4},
		AffectedResources: []audit.AffectedResource{{Type: "line_item", ID: "li-1"}},
		ErrorCode:         strPtr("E1"),
		ErrorMessage:      strPtr("boom"),
		CreatedAt:         createdAt,
	}

	got := ToResponse(entry)

	if got.ID != id.String() {
		t.Errorf("ID = %q, want %q", got.ID, id.String())
	}
	if got.ResourceType != entry.ResourceType {
		t.Errorf("ResourceType = %q, want %q", got.ResourceType, entry.ResourceType)
	}
	if got.ResourceID != entry.ResourceID {
		t.Errorf("ResourceID = %q, want %q", got.ResourceID, entry.ResourceID)
	}
	if got.AggregateType != entry.AggregateType {
		t.Errorf("AggregateType = %v, want %v (same pointer)", got.AggregateType, entry.AggregateType)
	}
	if got.AggregateID != entry.AggregateID {
		t.Errorf("AggregateID = %v, want %v (same pointer)", got.AggregateID, entry.AggregateID)
	}
	if got.OperationCategory != entry.OperationCategory {
		t.Errorf("OperationCategory = %q, want %q", got.OperationCategory, entry.OperationCategory)
	}
	if got.OperationName != entry.OperationName {
		t.Errorf("OperationName = %q, want %q", got.OperationName, entry.OperationName)
	}
	if got.Status != entry.Status {
		t.Errorf("Status = %q, want %q", got.Status, entry.Status)
	}
	if got.ActorID != entry.ActorID {
		t.Errorf("ActorID = %q, want %q", got.ActorID, entry.ActorID)
	}
	if got.Username != entry.Username {
		t.Errorf("Username = %q, want %q", got.Username, entry.Username)
	}
	if got.Source != entry.Source {
		t.Errorf("Source = %q, want %q", got.Source, entry.Source)
	}
	if got.RequestID != entry.RequestID {
		t.Errorf("RequestID = %v, want %v (same pointer)", got.RequestID, entry.RequestID)
	}
	if got.IP != entry.IP {
		t.Errorf("IP = %q, want %q", got.IP, entry.IP)
	}
	if got.UserAgent != entry.UserAgent {
		t.Errorf("UserAgent = %q, want %q", got.UserAgent, entry.UserAgent)
	}
	if got.ObjectRepr != entry.ObjectRepr {
		t.Errorf("ObjectRepr = %q, want %q", got.ObjectRepr, entry.ObjectRepr)
	}
	if got.InputPayload["a"] != 1 {
		t.Errorf("InputPayload not mapped: %v", got.InputPayload)
	}
	if got.BeforeSnapshot["b"] != 2 {
		t.Errorf("BeforeSnapshot not mapped: %v", got.BeforeSnapshot)
	}
	if got.AfterSnapshot["c"] != 3 {
		t.Errorf("AfterSnapshot not mapped: %v", got.AfterSnapshot)
	}
	if got.ChangedFields["d"] != 4 {
		t.Errorf("ChangedFields not mapped: %v", got.ChangedFields)
	}
	if len(got.AffectedResources) != 1 || got.AffectedResources[0] != (audit.AffectedResource{Type: "line_item", ID: "li-1"}) {
		t.Errorf("AffectedResources = %+v, want [{line_item li-1}]", got.AffectedResources)
	}
	if got.ErrorCode != entry.ErrorCode {
		t.Errorf("ErrorCode = %v, want %v (same pointer)", got.ErrorCode, entry.ErrorCode)
	}
	if got.ErrorMessage != entry.ErrorMessage {
		t.Errorf("ErrorMessage = %v, want %v (same pointer)", got.ErrorMessage, entry.ErrorMessage)
	}
	if !got.CreatedAt.Equal(entry.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, entry.CreatedAt)
	}
}

func TestToResponse_NilOptionalPointersStayNil(t *testing.T) {
	entry := &audit.Entry{
		ID:                uuid.New(),
		ResourceType:      "invoice",
		ResourceID:        "inv-1",
		OperationCategory: "create",
		Status:            "success",
		CreatedAt:         time.Now(),
	}

	got := ToResponse(entry)

	if got.AggregateType != nil {
		t.Errorf("AggregateType = %v, want nil", got.AggregateType)
	}
	if got.AggregateID != nil {
		t.Errorf("AggregateID = %v, want nil", got.AggregateID)
	}
	if got.RequestID != nil {
		t.Errorf("RequestID = %v, want nil", got.RequestID)
	}
	if got.ErrorCode != nil {
		t.Errorf("ErrorCode = %v, want nil", got.ErrorCode)
	}
	if got.ErrorMessage != nil {
		t.Errorf("ErrorMessage = %v, want nil", got.ErrorMessage)
	}
	if got.AffectedResources != nil {
		t.Errorf("AffectedResources = %v, want nil", got.AffectedResources)
	}
}

func TestToResponseList_PreservesOrder(t *testing.T) {
	entries := []audit.Entry{
		{ID: uuid.New(), ResourceType: "a"},
		{ID: uuid.New(), ResourceType: "b"},
		{ID: uuid.New(), ResourceType: "c"},
	}

	got := ToResponseList(entries)

	if len(got) != len(entries) {
		t.Fatalf("len = %d, want %d", len(got), len(entries))
	}
	for i, e := range entries {
		if got[i].ResourceType != e.ResourceType {
			t.Errorf("index %d: ResourceType = %q, want %q", i, got[i].ResourceType, e.ResourceType)
		}
		if got[i].ID != e.ID.String() {
			t.Errorf("index %d: ID = %q, want %q", i, got[i].ID, e.ID.String())
		}
	}
}

// TestToResponseList_EmptyInputReturnsNonNilEmptySlice documents the chosen
// behavior: an empty (or nil) input slice returns a non-nil, empty output
// slice, so JSON-encoding the result produces `[]` rather than `null`.
func TestToResponseList_EmptyInputReturnsNonNilEmptySlice(t *testing.T) {
	got := ToResponseList([]audit.Entry{})
	if got == nil {
		t.Fatal("ToResponseList(empty) = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}

	got = ToResponseList(nil)
	if got == nil {
		t.Fatal("ToResponseList(nil) = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}
