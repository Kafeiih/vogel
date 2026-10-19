package audit

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kafeiih/vogel/logger"
)

// logger keeps its own copy of the default denylist because it cannot import
// audit. A key added to one list and not the other would let a secret that
// audit redacts reach the logs, or the other way round.
func TestDefaultRedactedKeys_MatchLogger(t *testing.T) {
	assert.ElementsMatch(t, defaultRedactedKeys, logger.DefaultRedactedKeys(),
		"audit.defaultRedactedKeys and logger.DefaultRedactedKeys drifted; update both")
}

// Both packages replace a sensitive value with the same marker.
func TestRedactedValue_MatchesLogger(t *testing.T) {
	assert.Equal(t, RedactedValue, logger.RedactedValue)
}
