package response_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kafeiih/vogel/httpx/response"
)

// TestCodeExportTooLarge_Value pins the wire value: clients match on it, so it
// is a public API contract.
func TestCodeExportTooLarge_Value(t *testing.T) {
	assert.Equal(t, "EXPORT_TOO_LARGE", response.CodeExportTooLarge)
}
