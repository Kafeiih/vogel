package postgres

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyTLSConfig_PreservesServerName is the regression test for the bug
// where assigning a fresh &tls.Config{} dropped the ServerName that
// pgxpool.ParseConfig had set from the DSN. Without ServerName, the TLS
// handshake against managed databases (RDS, Cloud SQL) fails certificate
// validation because Go cannot match the cert CN against the connection host.
func TestApplyTLSConfig_PreservesServerName(t *testing.T) {
	const host = "psql.example.com"
	dsn := "postgres://u:p@" + host + ":5432/db?sslmode=require"

	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	require.NotNil(t, cfg.ConnConfig.TLSConfig,
		"ParseConfig must set TLSConfig when DSN has sslmode=require")

	originalServerName := cfg.ConnConfig.TLSConfig.ServerName
	require.Equal(t, host, originalServerName,
		"ParseConfig should set ServerName from the DSN host")

	err = applyTLSConfig(cfg, true)
	require.NoError(t, err)

	assert.Equal(t, originalServerName, cfg.ConnConfig.TLSConfig.ServerName,
		"applyTLSConfig must preserve the ServerName set by ParseConfig")
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.ConnConfig.TLSConfig.MinVersion,
		"applyTLSConfig must enforce TLS 1.2 minimum")
}

// TestApplyTLSConfig_NoOpWhenRequireTLSFalse confirms development mode keeps
// the connection plain (no TLS forced from the client side) and returns no error.
func TestApplyTLSConfig_NoOpWhenRequireTLSFalse(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://u:p@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)
	require.Nil(t, cfg.ConnConfig.TLSConfig,
		"ParseConfig should leave TLSConfig nil for sslmode=disable")

	err = applyTLSConfig(cfg, false)
	require.NoError(t, err)

	assert.Nil(t, cfg.ConnConfig.TLSConfig,
		"applyTLSConfig must not create TLSConfig when requireTLS is false")
}

// TestApplyTLSConfig_RequireTLSTrueWithDisabledDSN_FailsFast is the regression
// test for FIX 3: previously, RequireTLS=true against a DSN with
// sslmode=disable silently mutated a TLSConfig object that pgconn never
// actually used, producing a plaintext connection while the caller believed
// TLS had been enforced. It must now return a clear error instead.
func TestApplyTLSConfig_RequireTLSTrueWithDisabledDSN_FailsFast(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://u:p@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)
	require.Nil(t, cfg.ConnConfig.TLSConfig)

	err = applyTLSConfig(cfg, true)

	require.Error(t, err, "RequireTLS=true with a TLS-disabling DSN must fail, not silently no-op")
	assert.Contains(t, err.Error(), "RequireTLS")
	assert.Contains(t, err.Error(), "sslmode")
}

// TestNewPool_RequireTLSTrueWithDisabledDSN_ReturnsErrorBeforeConnecting verifies
// that NewPool itself surfaces the TLS configuration error without attempting
// to open a connection (no network access required for this test).
func TestNewPool_RequireTLSTrueWithDisabledDSN_ReturnsErrorBeforeConnecting(t *testing.T) {
	cfg := Config{
		URL:        "postgres://u:p@localhost:5432/db?sslmode=disable",
		RequireTLS: true,
	}
	pool, err := NewPool(t.Context(), cfg, nil)
	require.Error(t, err)
	assert.Nil(t, pool)
	assert.Contains(t, err.Error(), "RequireTLS")
}

func TestResolveTimeoutMillis(t *testing.T) {
	t.Run("zero uses default", func(t *testing.T) {
		assert.Equal(t, (30 * time.Second).Milliseconds(), resolveTimeoutMillis(0, 30*time.Second))
	})

	t.Run("positive value is used as-is", func(t *testing.T) {
		assert.Equal(t, (10 * time.Second).Milliseconds(), resolveTimeoutMillis(10*time.Second, 30*time.Second))
	})

	t.Run("negative value disables the timeout (0)", func(t *testing.T) {
		assert.Equal(t, int64(0), resolveTimeoutMillis(-1, 30*time.Second))
	})
}
