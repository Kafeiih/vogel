package httpx

import (
	"net/http"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/request"
)

// operationCategories and statuses are the allowed values for the
// operation_category and status query parameters. They are derived from
// audit's own constants rather than repeated as literals: a value renamed
// there stops compiling here, and a value added there is picked up without
// this package being touched.
var (
	operationCategories = stringsOf(audit.Actions())
	statuses            = stringsOf(audit.Statuses())
)

// stringsOf converts a slice of string-kinded constants to plain strings,
// which is what request.Validator.Enum compares against.
func stringsOf[T ~string](vals []T) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}
	return out
}

// FiltersFromRequest parses audit.Filters from r's query parameters,
// recording any validation error on v (an invalid resource_id, an
// out-of-range operation_category or status, a malformed from/to). It does
// not check v.HasErrors() itself — the caller decides when to check and
// respond, so this composes with validating other request state before a
// response is written.
//
// resource_id is parsed as a UUID via request.Validator.UUIDQuery. This is
// a deliberate correction relative to some ports of this handler that leave
// resource_id an unparsed string filter.
//
// limit defaults to 20 and is capped at 100; offset defaults to 0.
func FiltersFromRequest(v *request.Validator, r *http.Request) audit.Filters {
	q := r.URL.Query()

	limit := v.MaxInt("limit", v.IntQuery(r, "limit", 20), 100)
	offset := v.IntQuery(r, "offset", 0)
	category := v.Enum("operation_category", q.Get("operation_category"), operationCategories)
	status := v.Enum("status", q.Get("status"), statuses)
	from := v.TimeQuery(r, "from")
	to := v.TimeQuery(r, "to")
	resourceID := v.UUIDQuery(r, "resource_id")

	return audit.Filters{
		ActorID:           q.Get("actor_id"),
		ResourceType:      q.Get("resource_type"),
		ResourceID:        resourceID,
		OperationCategory: category,
		OperationName:     q.Get("operation_name"),
		Status:            status,
		AggregateType:     q.Get("aggregate_type"),
		AggregateID:       q.Get("aggregate_id"),
		From:              from,
		To:                to,
		Limit:             limit,
		Offset:            offset,
	}
}
