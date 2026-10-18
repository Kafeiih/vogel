//go:build integration

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/storage"
)

// pdfBody is the smallest content http.DetectContentType reads as a PDF.
const pdfBody = "%PDF-1.4\n%fake document body\n"

// newValidateWorker builds the validate_upload worker over the same
// dependencies as h, the way main.go wires it.
func newValidateWorker(h *DocumentHandler) *ValidateUploadWorker {
	return &ValidateUploadWorker{
		storage:  h.storage,
		store:    h.store,
		recorder: h.recorder,
		tx:       h.tx,
		pool:     h.pool,
		logger:   h.logger,
	}
}

// pendingUpload creates a document, stores body under a fresh upload key
// (unless body is nil) and confirms that key as pending, returning both.
func pendingUpload(t *testing.T, h *DocumentHandler, body *string) (Document, string) {
	t.Helper()
	ctx := context.Background()
	doc := createTestDocument(t, h)
	key := uploadKeyPrefix(doc.ID) + uuid.NewString()
	if body != nil {
		require.NoError(t, h.storage.Upload(ctx, &storage.UploadInput{Key: key, Body: strings.NewReader(*body), ContentType: "application/pdf"}))
	}
	require.NoError(t, h.store.AttachPendingFile(ctx, h.pool, doc.ID, key))
	return doc, key
}

func runValidate(t *testing.T, h *DocumentHandler, docID uuid.UUID, key string) {
	t.Helper()
	err := newValidateWorker(h).Work(context.Background(), &river.Job[ValidateUploadArgs]{
		Args: ValidateUploadArgs{DocumentID: docID.String(), Key: key},
	})
	require.NoError(t, err)
}

// validationAudit returns the source and the after snapshot's file_status of
// every document_validate_file audit row for docID.
func validationAudit(t *testing.T, h *DocumentHandler, docID uuid.UUID) [][2]string {
	t.Helper()
	rows, err := h.pool.Query(context.Background(), `
		SELECT source, after_snapshot->>'file_status' FROM audit_log
		WHERE resource_type = 'document' AND resource_id = $1 AND operation_name = 'document_validate_file'
		ORDER BY created_at`, docID.String())
	require.NoError(t, err)
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var source, status string
		require.NoError(t, rows.Scan(&source, &status))
		out = append(out, [2]string{source, status})
	}
	require.NoError(t, rows.Err())
	return out
}

func TestValidateUpload(t *testing.T) {
	h, _ := newIntegrationHandler(t)
	ctx := context.Background()
	pdf := pdfBody
	html := "<!DOCTYPE html><html><script>alert(1)</script></html>"

	t.Run("missing object is rejected", func(t *testing.T) {
		doc, key := pendingUpload(t, h, nil)

		runValidate(t, h, doc.ID, key)

		got, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, FileStatusRejected, got.FileStatus)
		assert.Empty(t, got.FileContentType)
		assert.Equal(t, [][2]string{{"worker", FileStatusRejected}}, validationAudit(t, h, doc.ID))
	})

	t.Run("disallowed content is deleted and rejected", func(t *testing.T) {
		doc, key := pendingUpload(t, h, &html)

		runValidate(t, h, doc.ID, key)

		got, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, FileStatusRejected, got.FileStatus)
		assert.Empty(t, got.FileContentType)
		_, dlErr := h.storage.Download(ctx, key)
		assert.ErrorIs(t, dlErr, errObjectNotFound, "the rejected object is deleted")
		assert.Equal(t, [][2]string{{"worker", FileStatusRejected}}, validationAudit(t, h, doc.ID))
	})

	t.Run("allowed content is ready with the sniffed type", func(t *testing.T) {
		doc, key := pendingUpload(t, h, &pdf)

		runValidate(t, h, doc.ID, key)

		got, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, FileStatusReady, got.FileStatus)
		assert.Equal(t, "application/pdf", got.FileContentType)
		assert.Equal(t, key, got.StorageKey)
		out, err := h.storage.Download(ctx, key)
		require.NoError(t, err, "an accepted object is kept")
		_ = out.Body.Close()
		assert.Equal(t, [][2]string{{"worker", FileStatusReady}}, validationAudit(t, h, doc.ID))
	})

	t.Run("a stale key never overwrites the newer upload", func(t *testing.T) {
		doc, oldKey := pendingUpload(t, h, &pdf)
		newKey := uploadKeyPrefix(doc.ID) + uuid.NewString()
		require.NoError(t, h.store.AttachPendingFile(ctx, h.pool, doc.ID, newKey))

		runValidate(t, h, doc.ID, oldKey)

		got, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, newKey, got.StorageKey)
		assert.Equal(t, FileStatusPending, got.FileStatus, "the newer upload still awaits its own job")
		assert.Empty(t, validationAudit(t, h, doc.ID), "a verdict that changed nothing is not audited")
	})

	t.Run("a document deleted meanwhile is a no-op", func(t *testing.T) {
		doc, key := pendingUpload(t, h, &pdf)
		require.NoError(t, h.store.Delete(ctx, h.pool, doc.ID))

		runValidate(t, h, doc.ID, key)

		assert.Empty(t, validationAudit(t, h, doc.ID))
	})
}
