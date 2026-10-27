package openapitest_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"

	"github.com/kafeiih/vogel/httpx/openapi/openapitest"
	"github.com/kafeiih/vogel/httpx/response"
)

// recorder captures failures instead of failing the real test.
type recorder struct {
	testing.TB
	errors []string
	fatal  bool
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	r.fatal = true
}

const spec = `
openapi: 3.1.0
paths:
  /audit/:
    summary: audit entries
    parameters: []
    get:
      operationId: listAuditEntries
  /audit/{id}:
    get:
      operationId: getAuditEntry
  /cases/{id}/claim:
    post:
      operationId: claimCase
components:
  schemas:
    ErrorResponse:
      type: object
      required: [success, message, code]
      properties:
        success: {type: boolean}
        message: {type: string}
        code: {type: string}
        error: {type: string}
        field: {type: string}
        request_id: {type: string}
`

func noop(http.ResponseWriter, *http.Request) {}

func router(extra func(chi.Router)) chi.Router {
	r := chi.NewRouter()
	r.Get("/healthz", noop)
	r.Route("/v1", func(r chi.Router) {
		r.Route("/audit", func(r chi.Router) {
			r.Get("/", noop)
			r.Get("/{id:[0-9a-f-]+}", noop)
		})
		r.Post("/cases/{id}/claim", noop)
		if extra != nil {
			extra(r)
		}
	})
	return r
}

func TestRoutesMatchSpec_PassesWhenBothDescribeTheSameOperations(t *testing.T) {
	rec := &recorder{TB: t}

	openapitest.RoutesMatchSpec(rec, []byte(spec), router(nil), openapitest.Prefix("/v1"), openapitest.Ignore("GET /healthz"))

	assert.Empty(t, rec.errors)
}

func TestRoutesMatchSpec_ReportsDriftInBothDirections(t *testing.T) {
	rec := &recorder{TB: t}
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Get("/audit/", noop)
		r.Delete("/audit/{id}", noop)
		r.Post("/cases/{id}/claim", noop)
	})

	openapitest.RoutesMatchSpec(rec, []byte(spec), r, openapitest.Prefix("/v1"))

	assert.ElementsMatch(t, []string{
		"GET /v1/audit/{id} is in the OpenAPI spec but not registered on the router",
		"DELETE /v1/audit/{id} is served but not described in the OpenAPI spec",
	}, rec.errors)
}

func TestRoutesMatchSpec_ReportsUnignoredRoutes(t *testing.T) {
	rec := &recorder{TB: t}

	openapitest.RoutesMatchSpec(rec, []byte(spec), router(nil), openapitest.Prefix("/v1"))

	assert.Equal(t, []string{"GET /healthz is served but not described in the OpenAPI spec"}, rec.errors)
}

func TestRoutesMatchSpec_FailsOnAnUnreadableSpec(t *testing.T) {
	rec := &recorder{TB: t}

	openapitest.RoutesMatchSpec(rec, []byte("paths: ["), router(nil))

	assert.True(t, rec.fatal)
	assert.True(t, strings.HasPrefix(rec.errors[0], "parse OpenAPI spec:"), rec.errors[0])
}

func TestSchemaMatchesType_PassesForAMatchingSchema(t *testing.T) {
	rec := &recorder{TB: t}

	openapitest.SchemaMatchesType[response.ErrorResponse](rec, []byte(spec), "ErrorResponse")

	assert.Empty(t, rec.errors)
}

type entry struct {
	ID      string `json:"id"`
	Actor   string `json:"actor,omitempty"`
	Payload string `json:"payload"`
	secret  string
	Skipped string `json:"-"`
}

func TestSchemaMatchesType_ReportsPropertyAndRequiredDrift(t *testing.T) {
	rec := &recorder{TB: t}
	doc := `
components:
  schemas:
    Entry:
      required: [id, actor]
      properties:
        id: {type: string}
        actor: {type: string}
        extra: {type: string}
`

	openapitest.SchemaMatchesType[entry](rec, []byte(doc), "Entry")

	assert.Equal(t, []string{
		"schema Entry properties = [actor extra id], want the Go type's [actor id payload]",
		"schema Entry required = [actor id], want the Go type's [id payload]",
	}, rec.errors)
	_ = entry{}.secret
}

func TestSchemaMatchesType_FailsOnAMissingSchema(t *testing.T) {
	rec := &recorder{TB: t}

	openapitest.SchemaMatchesType[entry](rec, []byte(spec), "Entry")

	assert.True(t, rec.fatal)
	assert.Equal(t, []string{"schema Entry missing from the OpenAPI spec"}, rec.errors)
}
