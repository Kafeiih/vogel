package http

import (
	"testing"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/audit/httpx"
	"github.com/kafeiih/vogel/httpx/openapi/openapitest"
	"github.com/kafeiih/vogel/httpx/response"

	"github.com/kafeiih/vogel/examples/boilerplate/api"
	// vogel:component bpms begin
	"github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
	// vogel:component bpms end
)

func TestSpecAndRouterMatch(t *testing.T) {
	openapitest.RoutesMatchSpec(t, api.Spec(), newMatrixRouter(t),
		openapitest.Ignore("GET /healthz", "GET /readyz", "GET /livez", "GET /openapi.yaml"))
}

func TestSchemasMatchVogelTypes(t *testing.T) {
	openapitest.SchemaMatchesType[response.ErrorResponse](t, api.Spec(), "ErrorResponse")
	openapitest.SchemaMatchesType[response.ValidationErrorResponse](t, api.Spec(), "ValidationErrorResponse")
	openapitest.SchemaMatchesType[response.ListMeta](t, api.Spec(), "ListMeta")
	openapitest.SchemaMatchesType[response.SuccessResponse](t, api.Spec(), "AuditEntryResponse")
	openapitest.SchemaMatchesType[response.ListResponse](t, api.Spec(), "AuditEntryListResponse")
	openapitest.SchemaMatchesType[httpx.Response](t, api.Spec(), "AuditEntry")
	openapitest.SchemaMatchesType[audit.AffectedResource](t, api.Spec(), "AffectedResource")
	// vogel:component bpms begin
	for _, name := range []string{"PublishedProcessResponse", "ProcessVersionResponse", "CaseResponse", "CaseListResponse", "CaseViewResponse"} {
		openapitest.SchemaMatchesType[response.SuccessResponse](t, api.Spec(), name)
	}
	openapitest.SchemaMatchesType[process.CaseValue](t, api.Spec(), "CaseValue")
	// vogel:component bpms end
}
