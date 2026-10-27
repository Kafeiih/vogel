package http

import (
	"testing"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/audit/httpx"
	"github.com/kafeiih/vogel/httpx/openapi/openapitest"
	"github.com/kafeiih/vogel/httpx/response"

	"github.com/kafeiih/vogel/examples/boilerplate/api"
)

func TestSpecAndRouterMatch(t *testing.T) {
	openapitest.RoutesMatchSpec(t, api.Spec(), newMatrixRouter(t),
		openapitest.Ignore("GET /healthz", "GET /readyz", "GET /livez", "GET /openapi.yaml"),
		// vogel:component bpms begin
		openapitest.Ignore(
			"POST /processes", "GET /processes/{name}", "POST /processes/{name}/cases",
			"GET /inbox", "GET /cases/{id}", "POST /cases/{id}/claim", "POST /cases/{id}/submit",
		),
		// vogel:component bpms end
	)
}

func TestSchemasMatchVogelTypes(t *testing.T) {
	openapitest.SchemaMatchesType[response.ErrorResponse](t, api.Spec(), "ErrorResponse")
	openapitest.SchemaMatchesType[response.ValidationErrorResponse](t, api.Spec(), "ValidationErrorResponse")
	openapitest.SchemaMatchesType[response.ListMeta](t, api.Spec(), "ListMeta")
	openapitest.SchemaMatchesType[httpx.Response](t, api.Spec(), "AuditEntry")
	openapitest.SchemaMatchesType[audit.AffectedResource](t, api.Spec(), "AffectedResource")
}
