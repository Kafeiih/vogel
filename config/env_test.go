package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/config"
)

func TestString(t *testing.T) {
	t.Run("set value wins", func(t *testing.T) {
		t.Setenv("VOGEL_TEST_STRING", "hello")
		assert.Equal(t, "hello", config.String("VOGEL_TEST_STRING", "default"))
	})

	t.Run("unset returns default", func(t *testing.T) {
		assert.Equal(t, "default", config.String("VOGEL_TEST_STRING_UNSET", "default"))
	})
}

func TestRequire(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		t.Setenv("VOGEL_TEST_REQUIRED", "value")
		v, err := config.Require("VOGEL_TEST_REQUIRED")
		require.NoError(t, err)
		assert.Equal(t, "value", v)
	})

	t.Run("missing returns a useful error", func(t *testing.T) {
		_, err := config.Require("VOGEL_TEST_REQUIRED_MISSING")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "VOGEL_TEST_REQUIRED_MISSING")
	})
}

func TestBool(t *testing.T) {
	t.Run("true", func(t *testing.T) {
		t.Setenv("VOGEL_TEST_BOOL", "true")
		assert.True(t, config.Bool("VOGEL_TEST_BOOL", false))
	})

	t.Run("unset returns default", func(t *testing.T) {
		assert.True(t, config.Bool("VOGEL_TEST_BOOL_UNSET", true))
	})

	t.Run("invalid falls back to default", func(t *testing.T) {
		t.Setenv("VOGEL_TEST_BOOL_BAD", "not-a-bool")
		assert.False(t, config.Bool("VOGEL_TEST_BOOL_BAD", false))
	})
}

func TestInt(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		t.Setenv("VOGEL_TEST_INT", "42")
		v, err := config.Int("VOGEL_TEST_INT", 0)
		require.NoError(t, err)
		assert.Equal(t, 42, v)
	})

	t.Run("unset returns default", func(t *testing.T) {
		v, err := config.Int("VOGEL_TEST_INT_UNSET", 7)
		require.NoError(t, err)
		assert.Equal(t, 7, v)
	})

	t.Run("invalid returns error", func(t *testing.T) {
		t.Setenv("VOGEL_TEST_INT_BAD", "abc")
		_, err := config.Int("VOGEL_TEST_INT_BAD", 0)
		require.Error(t, err)
	})
}

func TestInt32(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		t.Setenv("VOGEL_TEST_INT32", "25")
		v, err := config.Int32("VOGEL_TEST_INT32", 0)
		require.NoError(t, err)
		assert.Equal(t, int32(25), v)
	})

	t.Run("invalid returns error", func(t *testing.T) {
		t.Setenv("VOGEL_TEST_INT32_BAD", "not-int")
		_, err := config.Int32("VOGEL_TEST_INT32_BAD", 0)
		require.Error(t, err)
	})
}

func TestDuration(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		t.Setenv("VOGEL_TEST_DURATION", "30s")
		v, err := config.Duration("VOGEL_TEST_DURATION", 0)
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, v)
	})

	t.Run("unset returns default", func(t *testing.T) {
		v, err := config.Duration("VOGEL_TEST_DURATION_UNSET", 5*time.Second)
		require.NoError(t, err)
		assert.Equal(t, 5*time.Second, v)
	})

	t.Run("invalid returns error", func(t *testing.T) {
		t.Setenv("VOGEL_TEST_DURATION_BAD", "not-a-duration")
		_, err := config.Duration("VOGEL_TEST_DURATION_BAD", 0)
		require.Error(t, err)
	})
}

func TestStringSlice(t *testing.T) {
	t.Run("splits, trims, and drops empties", func(t *testing.T) {
		t.Setenv("VOGEL_TEST_SLICE", "a, b ,, c")
		assert.Equal(t, []string{"a", "b", "c"}, config.StringSlice("VOGEL_TEST_SLICE", ","))
	})

	t.Run("unset returns nil", func(t *testing.T) {
		assert.Nil(t, config.StringSlice("VOGEL_TEST_SLICE_UNSET", ","))
	})
}
