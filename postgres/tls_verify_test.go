package postgres

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTestCA writes a throwaway self-signed CA certificate and returns its
// path, so DSNs with sslrootcert parse without a real CA bundle.
func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "vogel test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	return path
}

// TestApplyTLSConfig_RequireTLSDemandsServerVerification covers every
// sslmode against RequireTLS=true. Only modes under which pgx verifies the
// server certificate are accepted: verify-ca, verify-full, and require with
// sslrootcert (which libpq and pgx treat as verify-ca). disable, allow,
// prefer and a bare require either allow plaintext or accept any
// certificate, so a man in the middle could read the traffic.
func TestApplyTLSConfig_RequireTLSDemandsServerVerification(t *testing.T) {
	ca := url.QueryEscape(writeTestCA(t))
	const base = "postgres://u:p@db.example.com:5432/db"

	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{name: "sslmode absent defaults to prefer", query: "", wantErr: true},
		{name: "disable", query: "?sslmode=disable", wantErr: true},
		{name: "allow", query: "?sslmode=allow", wantErr: true},
		{name: "prefer", query: "?sslmode=prefer", wantErr: true},
		{name: "require without root cert", query: "?sslmode=require", wantErr: true},
		{name: "require with root cert verifies the chain", query: "?sslmode=require&sslrootcert=" + ca, wantErr: false},
		{name: "verify-ca", query: "?sslmode=verify-ca&sslrootcert=" + ca, wantErr: false},
		{name: "verify-full with system roots", query: "?sslmode=verify-full", wantErr: false},
		{name: "verify-full with root cert", query: "?sslmode=verify-full&sslrootcert=" + ca, wantErr: false},
		{name: "multi-host verify-full", query: "?sslmode=verify-full", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsn := base + tt.query
			if tt.name == "multi-host verify-full" {
				dsn = "postgres://u:p@db1.example.com:5432,db2.example.com:5432/db" + tt.query
			}
			t.Setenv("PGSSLMODE", "")

			cfg, err := pgxpool.ParseConfig(dsn)
			require.NoError(t, err)

			err = applyTLSConfig(cfg, true)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "RequireTLS")
				assert.Contains(t, err.Error(), "verify-full")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cfg.ConnConfig.TLSConfig)
			assert.Equal(t, uint16(tls.VersionTLS12), cfg.ConnConfig.TLSConfig.MinVersion, "TLS 1.2 minimum")
			for _, fb := range cfg.ConnConfig.Fallbacks {
				require.NotNil(t, fb.TLSConfig)
				assert.Equal(t, uint16(tls.VersionTLS12), fb.TLSConfig.MinVersion, "TLS 1.2 minimum on fallbacks")
			}
		})
	}
}

// TestApplyTLSConfig_RequireTLSFalseAcceptsAnySSLMode keeps the zero-value
// behavior: without RequireTLS the DSN is taken as is.
func TestApplyTLSConfig_RequireTLSFalseAcceptsAnySSLMode(t *testing.T) {
	for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-full"} {
		t.Run(mode, func(t *testing.T) {
			cfg, err := pgxpool.ParseConfig("postgres://u:p@db.example.com:5432/db?sslmode=" + mode)
			require.NoError(t, err)
			assert.NoError(t, applyTLSConfig(cfg, false))
		})
	}
}
