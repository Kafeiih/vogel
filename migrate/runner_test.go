package migrate

import (
	"errors"
	"log/slog"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveLogger_DefaultsWhenNilOrUnset(t *testing.T) {
	assert.NotNil(t, resolveLogger(nil))
	assert.NotNil(t, resolveLogger([]Options{{}}))
}

func TestResolveLogger_ReturnsProvidedLogger(t *testing.T) {
	custom := slog.New(slog.DiscardHandler)
	got := resolveLogger([]Options{{Logger: custom}})
	assert.Same(t, custom, got)
}

func TestResolveLockID_DefaultsToZero(t *testing.T) {
	assert.Equal(t, int64(0), resolveLockID(nil))
	assert.Equal(t, int64(0), resolveLockID([]Options{{}}))
}

func TestResolveLockID_ReturnsConfiguredValue(t *testing.T) {
	assert.Equal(t, int64(42), resolveLockID([]Options{{LockID: 42}}))
}

func TestLockDisabled(t *testing.T) {
	assert.False(t, lockDisabled(nil))
	assert.False(t, lockDisabled([]Options{{}}))
	assert.True(t, lockDisabled([]Options{{DisableLock: true}}))
}

func TestResolveTableName_DefaultsToEmpty(t *testing.T) {
	assert.Equal(t, "", resolveTableName(nil))
	assert.Equal(t, "", resolveTableName([]Options{{}}))
}

func TestResolveTableName_ReturnsConfiguredValue(t *testing.T) {
	assert.Equal(t, "vogel_db_version", resolveTableName([]Options{{TableName: "vogel_db_version"}}))
}

// TestNewProvider_EmptyFS_ReturnsErrNoMigrations verifies that a filesystem
// with no migration files is rejected before any database round-trip is
// attempted (sql.Open with the pgx stdlib driver does not dial eagerly, and
// goose.NewProvider inspects the filesystem before touching the database).
func TestNewProvider_EmptyFS_ReturnsErrNoMigrations(t *testing.T) {
	_, err := newProvider("postgres://u:p@localhost:5/db", fstest.MapFS{}, []Options{{DisableLock: true}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, goose.ErrNoMigrations))
	assert.True(t, errors.Is(err, ErrNoMigrations))
}
