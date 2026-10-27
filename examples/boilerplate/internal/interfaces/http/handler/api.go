package handler

import "github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/apigen"

// API implements the operations of api/openapi.yaml; the generated
// StrictServerInterface makes a missing operation a compile error.
type API struct {
	*AuditHandler
}

var _ apigen.StrictServerInterface = API{}
