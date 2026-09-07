package httpx

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/request"
)

func TestFiltersFromRequest_Defaults(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/audit", nil)
	v := request.NewValidator()

	f := FiltersFromRequest(v, r)

	if v.HasErrors() {
		t.Fatalf("unexpected validation errors: %v", v.Errors())
	}
	if f.Limit != 20 {
		t.Errorf("Limit = %d, want 20", f.Limit)
	}
	if f.Offset != 0 {
		t.Errorf("Offset = %d, want 0", f.Offset)
	}
	if f.ResourceID != nil {
		t.Errorf("ResourceID = %v, want nil", f.ResourceID)
	}
}

func TestFiltersFromRequest_LimitAboveMaxIsCapped(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/audit?limit=500", nil)
	v := request.NewValidator()

	f := FiltersFromRequest(v, r)

	if v.HasErrors() {
		t.Fatalf("unexpected validation errors: %v", v.Errors())
	}
	if f.Limit != 100 {
		t.Errorf("Limit = %d, want 100 (capped)", f.Limit)
	}
}

func TestFiltersFromRequest_ValidResourceIDParsesToUUID(t *testing.T) {
	id := uuid.New()
	r := httptest.NewRequest(http.MethodGet, "/audit?resource_id="+id.String(), nil)
	v := request.NewValidator()

	f := FiltersFromRequest(v, r)

	if v.HasErrors() {
		t.Fatalf("unexpected validation errors: %v", v.Errors())
	}
	if f.ResourceID == nil {
		t.Fatal("ResourceID = nil, want parsed UUID")
	}
	if *f.ResourceID != id {
		t.Errorf("ResourceID = %v, want %v", *f.ResourceID, id)
	}
}

func TestFiltersFromRequest_InvalidResourceIDRecordsValidationError(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/audit?resource_id=not-a-uuid", nil)
	v := request.NewValidator()

	f := FiltersFromRequest(v, r)

	if !v.HasErrors() {
		t.Fatal("expected validation error for invalid resource_id")
	}
	if _, ok := v.Errors()["resource_id"]; !ok {
		t.Errorf("expected a validation error keyed \"resource_id\", got %v", v.Errors())
	}
	if f.ResourceID != nil {
		t.Errorf("ResourceID = %v, want nil on invalid input", f.ResourceID)
	}
}

func TestFiltersFromRequest_AbsentResourceIDLeavesFieldNil(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/audit", nil)
	v := request.NewValidator()

	f := FiltersFromRequest(v, r)

	if v.HasErrors() {
		t.Fatalf("unexpected validation errors: %v", v.Errors())
	}
	if f.ResourceID != nil {
		t.Errorf("ResourceID = %v, want nil", f.ResourceID)
	}
}

func TestFiltersFromRequest_InvalidEnumsRecordValidationErrors(t *testing.T) {
	tests := []struct {
		name  string
		query string
		field string
	}{
		{name: "invalid operation_category", query: "operation_category=bogus", field: "operation_category"},
		{name: "invalid status", query: "status=bogus", field: "status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/audit?"+tt.query, nil)
			v := request.NewValidator()

			FiltersFromRequest(v, r)

			if !v.HasErrors() {
				t.Fatal("expected a validation error")
			}
			if _, ok := v.Errors()[tt.field]; !ok {
				t.Errorf("expected a validation error keyed %q, got %v", tt.field, v.Errors())
			}
		})
	}
}

func TestFiltersFromRequest_ValidEnumsAreAccepted(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/audit?operation_category=create&status=success", nil)
	v := request.NewValidator()

	f := FiltersFromRequest(v, r)

	if v.HasErrors() {
		t.Fatalf("unexpected validation errors: %v", v.Errors())
	}
	if f.OperationCategory != "create" {
		t.Errorf("OperationCategory = %q, want create", f.OperationCategory)
	}
	if f.Status != "success" {
		t.Errorf("Status = %q, want success", f.Status)
	}
}

func TestFiltersFromRequest_FromToParseCorrectly(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	q := url.Values{}
	q.Set("from", from.Format(time.RFC3339))
	q.Set("to", to.Format(time.RFC3339))
	r := httptest.NewRequest(http.MethodGet, "/audit?"+q.Encode(), nil)
	v := request.NewValidator()

	f := FiltersFromRequest(v, r)

	if v.HasErrors() {
		t.Fatalf("unexpected validation errors: %v", v.Errors())
	}
	if f.From == nil || !f.From.Equal(from) {
		t.Errorf("From = %v, want %v", f.From, from)
	}
	if f.To == nil || !f.To.Equal(to) {
		t.Errorf("To = %v, want %v", f.To, to)
	}
}

func TestFiltersFromRequest_PassthroughStringFilters(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/audit?actor_id=a1&resource_type=invoice&operation_name=create_invoice&aggregate_type=order&aggregate_id=ord-1", nil)
	v := request.NewValidator()

	f := FiltersFromRequest(v, r)

	if v.HasErrors() {
		t.Fatalf("unexpected validation errors: %v", v.Errors())
	}
	if f.ActorID != "a1" {
		t.Errorf("ActorID = %q, want a1", f.ActorID)
	}
	if f.ResourceType != "invoice" {
		t.Errorf("ResourceType = %q, want invoice", f.ResourceType)
	}
	if f.OperationName != "create_invoice" {
		t.Errorf("OperationName = %q, want create_invoice", f.OperationName)
	}
	if f.AggregateType != "order" {
		t.Errorf("AggregateType = %q, want order", f.AggregateType)
	}
	if f.AggregateID != "ord-1" {
		t.Errorf("AggregateID = %q, want ord-1", f.AggregateID)
	}
}

// Ties the filter's accepted values to audit's declared ones in both
// directions. Adding an Action or Status to audit fails here, which is the
// point: whether a new domain value also becomes a filterable query value is a
// decision, and the endpoint's accepted set is a public API. Without this the
// two drift apart in whichever direction nobody was looking.
func TestAllowedValues_DerivedFromAuditConstants(t *testing.T) {
	if got := stringsOf(audit.Actions()); !slices.Equal(operationCategories, got) {
		t.Errorf("operationCategories = %v, want %v (derived from audit.Actions)", operationCategories, got)
	}
	if got := stringsOf(audit.Statuses()); !slices.Equal(statuses, got) {
		t.Errorf("statuses = %v, want %v (derived from audit.Statuses)", statuses, got)
	}

	// Pin the concrete wire values too: the derivation above passes even if
	// audit renamed every constant, and these strings are a public API.
	if want := []string{"create", "update", "delete", "execute"}; !slices.Equal(operationCategories, want) {
		t.Errorf("operationCategories = %v, want %v", operationCategories, want)
	}
	if want := []string{"success", "failed", "partial", "noop"}; !slices.Equal(statuses, want) {
		t.Errorf("statuses = %v, want %v", statuses, want)
	}
}
