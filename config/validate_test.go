package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/config"
)

func TestValidURL(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"valid https URL", "https://auth.institucion.gob", false},
		{"valid http URL with port", "http://localhost:3593", false},
		{"missing scheme", "localhost:3593", true},
		{"missing host", "https://", true},
		{"garbage", "not a url at all", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidURL("ISSUER_URL", tc.value)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "ISSUER_URL")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestIntRange(t *testing.T) {
	assert.NoError(t, config.IntRange("PORT", 50, 1, 100))
	assert.NoError(t, config.IntRange("PORT", 1, 1, 100), "min boundary is inclusive")
	assert.NoError(t, config.IntRange("PORT", 100, 1, 100), "max boundary is inclusive")

	err := config.IntRange("PORT", 200, 1, 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PORT")

	err = config.IntRange("PORT", 0, 1, 100)
	require.Error(t, err)
}

func TestOneOf(t *testing.T) {
	// This is the exact scenario the audit flagged: NOTIFICATION_PROVIDER
	// typo'd as "sendgrdi" must be caught, not silently accepted.
	assert.NoError(t, config.OneOf("NOTIFICATION_PROVIDER", "smtp", "smtp", "sendgrid"))
	assert.NoError(t, config.OneOf("NOTIFICATION_PROVIDER", "sendgrid", "smtp", "sendgrid"))

	err := config.OneOf("NOTIFICATION_PROVIDER", "sendgrdi", "smtp", "sendgrid")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NOTIFICATION_PROVIDER")
	assert.Contains(t, err.Error(), "sendgrdi")
}

func TestMinMax(t *testing.T) {
	assert.NoError(t, config.MinMax("DB_MIN_CONNS", 5, "DB_MAX_CONNS", 25))
	assert.NoError(t, config.MinMax("DB_MIN_CONNS", 5, "DB_MAX_CONNS", 5), "equal is valid")

	err := config.MinMax("DB_MIN_CONNS", 30, "DB_MAX_CONNS", 25)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_MIN_CONNS")
	assert.Contains(t, err.Error(), "DB_MAX_CONNS")
}

func TestErrors_SemanticValidatorsAccumulate(t *testing.T) {
	var e config.Errors
	e.ValidURL("ISSUER_URL", "not-a-url")
	e.IntRange("PORT", 99999, 1, 65535)
	e.OneOf("PROVIDER", "bogus", "smtp", "sendgrid")
	e.MinMax("MIN_CONNS", 30, "MAX_CONNS", 25)

	require.True(t, e.HasErrors())
	err := e.Err()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ISSUER_URL")
	assert.Contains(t, err.Error(), "PORT")
	assert.Contains(t, err.Error(), "PROVIDER")
	assert.Contains(t, err.Error(), "MIN_CONNS")
}
