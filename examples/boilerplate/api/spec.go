// Package api holds the HTTP contract: openapi.yaml is the source of truth,
// and the server code in internal/interfaces/http/apigen is generated from
// it (make generate).
package api

import _ "embed"

//go:generate go tool oapi-codegen -config oapi-codegen.yaml openapi.yaml

//go:embed openapi.yaml
var spec []byte

// Spec returns the raw OpenAPI document, as served at /openapi.yaml.
func Spec() []byte {
	return spec
}
