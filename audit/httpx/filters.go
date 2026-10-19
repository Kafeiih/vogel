package httpx

import (
	"net/http"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/request"
)

// MaxFilterLength is the maximum length, in characters, accepted for each
// free-text filter (actor_id, resource_type, operation_name, aggregate_type,
// aggregate_id). A longer value is a validation error (HTTP 400) instead of
// being passed to the repository: no legitimate identifier or type name is
// that long, and bounding it keeps an attacker from shipping arbitrarily
// large strings into the database query.
const MaxFilterLength = 256

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

// filterParams lists every query parameter FiltersFromRequest reads, in the
// order it reads them.
var filterParams = []string{
	"limit", "offset", "operation_category", "status", "from", "to", "resource_id",
	"actor_id", "resource_type", "operation_name", "aggregate_type", "aggregate_id",
}

// usedFilterParams returns the names in filterParams present in r's query,
// for logging: values and unrecognized keys are client data and are left out.
func usedFilterParams(r *http.Request) []string {
	q := r.URL.Query()
	used := []string{}
	for _, name := range filterParams {
		if q.Has(name) {
			used = append(used, name)
		}
	}
	return used
}

// FiltersFromRequest parses audit.Filters from r's query parameters,
// recording any validation error on v (an invalid resource_id, an
// out-of-range operation_category or status, a malformed from/to, a
// free-text filter longer than MaxFilterLength characters). It does
// not check v.HasErrors() itself — the caller decides when to check and
// respond, so this composes with validating other request state before a
// response is written.
//
// resource_id is parsed as a UUID via request.Validator.UUIDQuery. This is
// a deliberate correction relative to some ports of this handler that leave
// resource_id an unparsed string filter.
//
// limit defaults to 20 and is capped at 100; offset defaults to 0. A
// negative offset is a validation error here; one above audit.MaxListOffset
// is not, because the bound belongs to audit.Filters.Page, which a
// Repository applies before querying and reports as audit.ErrOffsetOutOfRange.
// Handler.List maps that error to 400; a caller that pairs this function with
// its own handler must do the same, or deep paging surfaces as a 500.
func FiltersFromRequest(v *request.Validator, r *http.Request) audit.Filters {
	q := r.URL.Query()

	limit := v.MaxInt("limit", v.IntQuery(r, "limit", 20), 100)
	offset := v.IntQuery(r, "offset", 0)
	category := v.Enum("operation_category", q.Get("operation_category"), operationCategories)
	status := v.Enum("status", q.Get("status"), statuses)
	from := v.TimeQuery(r, "from")
	to := v.TimeQuery(r, "to")
	resourceID := v.UUIDQuery(r, "resource_id")
	actorID := v.MaxLen("actor_id", q.Get("actor_id"), MaxFilterLength)
	resourceType := v.MaxLen("resource_type", q.Get("resource_type"), MaxFilterLength)
	operationName := v.MaxLen("operation_name", q.Get("operation_name"), MaxFilterLength)
	aggregateType := v.MaxLen("aggregate_type", q.Get("aggregate_type"), MaxFilterLength)
	aggregateID := v.MaxLen("aggregate_id", q.Get("aggregate_id"), MaxFilterLength)

	return audit.Filters{
		ActorID:           actorID,
		ResourceType:      resourceType,
		ResourceID:        resourceID,
		OperationCategory: category,
		OperationName:     operationName,
		Status:            status,
		AggregateType:     aggregateType,
		AggregateID:       aggregateID,
		From:              from,
		To:                to,
		Limit:             limit,
		Offset:            offset,
	}
}
