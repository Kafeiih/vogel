package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/config"
)

func TestErrors_ZeroValueIsUsable(t *testing.T) {
	var e config.Errors
	assert.False(t, e.HasErrors())
	assert.NoError(t, e.Err())
}

func TestErrors_ReportsEveryProblemAtOnce(t *testing.T) {
	// This is the behavior the source repositories were missing: config.Load()
	// returned on the *first* missing/invalid variable, so a developer fixing a
	// broken .env file discovered problems one restart at a time. Accumulating
	// them means every problem is visible in a single failed boot.
	var e config.Errors

	e.Require("VOGEL_TEST_MISSING_A")
	e.Require("VOGEL_TEST_MISSING_B")
	e.Int("VOGEL_TEST_ERRACC_INT", 0) // unset — no error
	t.Setenv("VOGEL_TEST_ERRACC_BAD_INT", "not-an-int")
	e.Int("VOGEL_TEST_ERRACC_BAD_INT", 0)

	require.True(t, e.HasErrors())
	err := e.Err()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VOGEL_TEST_MISSING_A")
	assert.Contains(t, err.Error(), "VOGEL_TEST_MISSING_B")
	assert.Contains(t, err.Error(), "VOGEL_TEST_ERRACC_BAD_INT")
}

func TestErrors_Require_RecordsAndReturnsEmpty(t *testing.T) {
	var e config.Errors
	got := e.Require("VOGEL_TEST_ERRACC_MISSING")
	assert.Equal(t, "", got)
	assert.True(t, e.HasErrors())
}

func TestErrors_Require_PresentDoesNotRecord(t *testing.T) {
	t.Setenv("VOGEL_TEST_ERRACC_PRESENT", "value")
	var e config.Errors
	got := e.Require("VOGEL_TEST_ERRACC_PRESENT")
	assert.Equal(t, "value", got)
	assert.False(t, e.HasErrors())
}

func TestErrors_Int32AndDuration_FallBackAndRecord(t *testing.T) {
	var e config.Errors

	t.Setenv("VOGEL_TEST_ERRACC_INT32_BAD", "nope")
	got32 := e.Int32("VOGEL_TEST_ERRACC_INT32_BAD", 5)
	assert.Equal(t, int32(5), got32)

	t.Setenv("VOGEL_TEST_ERRACC_DURATION_BAD", "nope")
	gotDur := e.Duration("VOGEL_TEST_ERRACC_DURATION_BAD", 0)
	assert.Equal(t, int64(0), int64(gotDur))

	assert.True(t, e.HasErrors())
}
