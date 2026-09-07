package httpx

import (
	"time"

	"github.com/kafeiih/vogel/audit"
)

// Response is the DTO returned by the audit HTTP handler for a single audit
// log entry. Field names and JSON tags mirror audit.Entry, the source of
// truth for what an audit entry contains.
type Response struct {
	ID string `json:"id"`

	ResourceType  string  `json:"resource_type"`
	ResourceID    string  `json:"resource_id"`
	AggregateType *string `json:"aggregate_type,omitempty"`
	AggregateID   *string `json:"aggregate_id,omitempty"`

	OperationCategory string `json:"operation_category"`
	OperationName     string `json:"operation_name"`
	Status            string `json:"status"`

	ActorID   string  `json:"actor_id"`
	Username  string  `json:"username"`
	Source    string  `json:"source"`
	RequestID *string `json:"request_id,omitempty"`
	IP        string  `json:"ip"`
	UserAgent string  `json:"user_agent"`

	ObjectRepr string `json:"object_repr,omitempty"`

	InputPayload      map[string]any           `json:"input_payload,omitempty"`
	BeforeSnapshot    map[string]any           `json:"before_snapshot,omitempty"`
	AfterSnapshot     map[string]any           `json:"after_snapshot,omitempty"`
	ChangedFields     map[string]any           `json:"changed_fields,omitempty"`
	AffectedResources []audit.AffectedResource `json:"affected_resources,omitempty"`

	ErrorCode    *string `json:"error_code,omitempty"`
	ErrorMessage *string `json:"error_message,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// ToResponse maps an audit.Entry to its HTTP DTO. Optional pointer fields
// (AggregateType, AggregateID, RequestID, ErrorCode, ErrorMessage) are
// passed through as-is, so a nil field on e stays nil on the result.
func ToResponse(e *audit.Entry) Response {
	return Response{
		ID:                e.ID.String(),
		ResourceType:      e.ResourceType,
		ResourceID:        e.ResourceID,
		AggregateType:     e.AggregateType,
		AggregateID:       e.AggregateID,
		OperationCategory: e.OperationCategory,
		OperationName:     e.OperationName,
		Status:            e.Status,
		ActorID:           e.ActorID,
		Username:          e.Username,
		Source:            e.Source,
		RequestID:         e.RequestID,
		IP:                e.IP,
		UserAgent:         e.UserAgent,
		ObjectRepr:        e.ObjectRepr,
		InputPayload:      e.InputPayload,
		BeforeSnapshot:    e.BeforeSnapshot,
		AfterSnapshot:     e.AfterSnapshot,
		ChangedFields:     e.ChangedFields,
		AffectedResources: e.AffectedResources,
		ErrorCode:         e.ErrorCode,
		ErrorMessage:      e.ErrorMessage,
		CreatedAt:         e.CreatedAt,
	}
}

// ToResponseList maps a slice of audit.Entry to their HTTP DTOs, preserving
// order. A nil or empty entries returns a non-nil, empty slice (never nil),
// so JSON-encoding the result produces `[]` rather than `null`.
func ToResponseList(entries []audit.Entry) []Response {
	result := make([]Response, len(entries))
	for i := range entries {
		result[i] = ToResponse(&entries[i])
	}
	return result
}
