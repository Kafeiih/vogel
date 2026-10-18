//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/migrate"
)

// Migration 002 adds the file lifecycle columns. A row that already had an
// object attached before it existed was served by the old proxying upload,
// which sniffed the content first, so it becomes 'ready'; every other row
// starts at 'none'. Down drops both columns again.
func TestMigration002_FileStatus(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	appFS, err := appMigrationsFS()
	require.NoError(t, err)

	require.NoError(t, migrate.UpTo(ctx, connStr, appFS, 1, migrate.Options{Logger: logger}))

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	withFile, withoutFile := uuid.New(), uuid.New()
	for id, key := range map[uuid.UUID]string{withFile: "documents/" + withFile.String(), withoutFile: ""} {
		_, err := pool.Exec(ctx, `INSERT INTO documents (id, title, kind, issued_at, storage_key, owner_id, created_at)
			VALUES ($1, 't', 'contract', '2026-01-02', $2, 'o', now())`, id, key)
		require.NoError(t, err)
	}

	require.NoError(t, migrate.Up(ctx, connStr, appFS, migrate.Options{Logger: logger}))

	for id, want := range map[uuid.UUID]string{withFile: FileStatusReady, withoutFile: FileStatusNone} {
		var status string
		var contentType *string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT file_status, file_content_type FROM documents WHERE id = $1`, id).Scan(&status, &contentType))
		assert.Equal(t, want, status, "file_status of %s", id)
		assert.Nil(t, contentType, "file_content_type starts NULL")
	}

	_, err = pool.Exec(ctx, `UPDATE documents SET file_status = 'bogus' WHERE id = $1`, withFile)
	require.Error(t, err, "the CHECK constraint rejects an unknown status")

	require.NoError(t, migrate.Down(ctx, connStr, appFS, migrate.Options{Logger: logger}))
	var columns int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'documents' AND column_name IN ('file_status', 'file_content_type')`).Scan(&columns))
	assert.Zero(t, columns, "Down drops both columns")
}

// A created document starts with no file, and GetByID reads the lifecycle
// columns back.
func TestDocumentStore_NewDocumentHasNoFile(t *testing.T) {
	h, pool := newIntegrationHandler(t)
	doc := createTestDocument(t, h)

	got, err := h.store.GetByID(context.Background(), pool, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, FileStatusNone, got.FileStatus)
	assert.Empty(t, got.FileContentType)
	assert.Equal(t, FileStatusNone, doc.FileStatus, "Create's response carries the status too")
}

// AttachPendingFile moves a document to a new key as pending, and
// ResolveUpload only lands while the row still points at the upload key it
// names, moving the row to the key it is given.
func TestDocumentStore_FileStatusFollowsTheCurrentKey(t *testing.T) {
	ctx := context.Background()
	h, pool := newIntegrationHandler(t)
	doc := createTestDocument(t, h)
	oldKey := uploadKeyPrefix(doc.ID) + uuid.NewString()
	newKey := uploadKeyPrefix(doc.ID) + uuid.NewString()

	require.NoError(t, h.store.AttachPendingFile(ctx, pool, doc.ID, oldKey))
	finalKey := fileKeyPrefix(doc.ID) + uuid.NewString()
	ok, err := h.store.ResolveUpload(ctx, pool, doc.ID, oldKey, finalKey, FileStatusReady, "application/pdf")
	require.NoError(t, err)
	require.True(t, ok)
	got, err := h.store.GetByID(ctx, pool, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, finalKey, got.StorageKey)
	assert.Equal(t, FileStatusReady, got.FileStatus)
	assert.Equal(t, "application/pdf", got.FileContentType)

	require.NoError(t, h.store.AttachPendingFile(ctx, pool, doc.ID, newKey))
	got, err = h.store.GetByID(ctx, pool, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, newKey, got.StorageKey)
	assert.Equal(t, FileStatusPending, got.FileStatus)
	assert.Empty(t, got.FileContentType, "a new upload clears the previously sniffed type")

	ok, err = h.store.ResolveUpload(ctx, pool, doc.ID, oldKey, oldKey, FileStatusRejected, "")
	require.NoError(t, err)
	assert.False(t, ok, "a verdict on the replaced key changes nothing")
	got, err = h.store.GetByID(ctx, pool, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, FileStatusPending, got.FileStatus)
}
