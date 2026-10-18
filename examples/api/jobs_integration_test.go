//go:build integration

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
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
		putObject(t, h, key, *body)
	}
	require.NoError(t, h.store.AttachPendingFile(ctx, h.pool, doc.ID, key))
	return doc, key
}

func putObject(t *testing.T, h *DocumentHandler, key, body string) {
	t.Helper()
	require.NoError(t, h.storage.Upload(context.Background(), &storage.UploadInput{
		Key: key, Body: strings.NewReader(body), ContentType: "application/pdf",
	}))
}

// validateJob wraps args the way River delivers them: first attempt of
// River's default 25.
func validateJob(args ValidateUploadArgs) *river.Job[ValidateUploadArgs] {
	return &river.Job[ValidateUploadArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: river.MaxAttemptsDefault},
		Args:   args,
	}
}

func runValidate(t *testing.T, h *DocumentHandler, args ValidateUploadArgs) {
	t.Helper()
	require.NoError(t, newValidateWorker(h).Work(context.Background(), validateJob(args)))
}

// readyFile creates a document whose file went through one accepted upload
// and returns it as stored.
func readyFile(t *testing.T, h *DocumentHandler) Document {
	t.Helper()
	pdf := pdfBody
	doc, key := pendingUpload(t, h, &pdf)
	runValidate(t, h, ValidateUploadArgs{DocumentID: doc.ID.String(), Key: key})
	got, err := h.store.GetByID(context.Background(), h.pool, doc.ID)
	require.NoError(t, err)
	require.Equal(t, FileStatusReady, got.FileStatus)
	return *got
}

// objectKeys lists the keys the fake bucket holds for docID, sorted.
func objectKeys(t *testing.T, h *DocumentHandler, docID uuid.UUID) []string {
	t.Helper()
	fake, ok := h.storage.(*fakeStorage)
	require.True(t, ok)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	keys := []string{}
	for k := range fake.objects {
		if strings.HasPrefix(k, "documents/"+docID.String()+"/") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func readObject(t *testing.T, h *DocumentHandler, key string) string {
	t.Helper()
	out, err := h.storage.Download(context.Background(), key)
	require.NoError(t, err)
	defer func() { _ = out.Body.Close() }()
	b, err := io.ReadAll(out.Body)
	require.NoError(t, err)
	return string(b)
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

// isFileKey reports whether key is a final file key for docID:
// documents/{id}/file/{uuid}.
func isFileKey(docID uuid.UUID, key string) bool {
	suffix, ok := strings.CutPrefix(key, fileKeyPrefix(docID))
	if !ok {
		return false
	}
	_, err := uuid.Parse(suffix)
	return err == nil
}

func TestValidateUpload(t *testing.T) {
	h, _ := newIntegrationHandler(t)
	ctx := context.Background()
	pdf := pdfBody
	html := "<!DOCTYPE html><html><script>alert(1)</script></html>"

	t.Run("missing object is rejected", func(t *testing.T) {
		doc, key := pendingUpload(t, h, nil)

		runValidate(t, h, ValidateUploadArgs{DocumentID: doc.ID.String(), Key: key})

		got, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, FileStatusRejected, got.FileStatus)
		assert.Equal(t, key, got.StorageKey)
		assert.Empty(t, got.FileContentType)
		assert.Equal(t, [][2]string{{"worker", FileStatusRejected}}, validationAudit(t, h, doc.ID))
	})

	t.Run("disallowed content is deleted and rejected", func(t *testing.T) {
		doc, key := pendingUpload(t, h, &html)

		runValidate(t, h, ValidateUploadArgs{DocumentID: doc.ID.String(), Key: key})

		got, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, FileStatusRejected, got.FileStatus)
		assert.Empty(t, got.FileContentType)
		_, dlErr := h.storage.Download(ctx, key)
		assert.ErrorIs(t, dlErr, storage.ErrNotFound, "the rejected object is deleted")
		assert.Empty(t, objectKeys(t, h, doc.ID), "no copy is made of rejected content")
		assert.Equal(t, [][2]string{{"worker", FileStatusRejected}}, validationAudit(t, h, doc.ID))
	})

	t.Run("allowed content is moved to a final key and ready", func(t *testing.T) {
		doc, key := pendingUpload(t, h, &pdf)

		runValidate(t, h, ValidateUploadArgs{DocumentID: doc.ID.String(), Key: key})

		got, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, FileStatusReady, got.FileStatus)
		assert.Equal(t, "application/pdf", got.FileContentType)
		require.True(t, isFileKey(doc.ID, got.StorageKey), "storage_key %q is a final documents/{id}/file/{uuid} key", got.StorageKey)
		assert.Equal(t, pdfBody, readObject(t, h, got.StorageKey))
		assert.Equal(t, []string{got.StorageKey}, objectKeys(t, h, doc.ID), "the upload object is deleted")
		assert.Equal(t, [][2]string{{"worker", FileStatusReady}}, validationAudit(t, h, doc.ID))

		// The upload URL may still be valid: whatever is PUT to the upload
		// key now never reaches the validated file.
		putObject(t, h, key, html)
		assert.Equal(t, pdfBody, readObject(t, h, got.StorageKey), "re-uploading to the upload key leaves the file untouched")
	})

	t.Run("a new ready file deletes the previous one", func(t *testing.T) {
		doc, firstKey := pendingUpload(t, h, &pdf)
		runValidate(t, h, ValidateUploadArgs{DocumentID: doc.ID.String(), Key: firstKey})
		first, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		previous := first.StorageKey

		secondKey := uploadKeyPrefix(doc.ID) + uuid.NewString()
		putObject(t, h, secondKey, pdf)
		require.NoError(t, h.store.AttachPendingFile(ctx, h.pool, doc.ID, secondKey))

		runValidate(t, h, ValidateUploadArgs{DocumentID: doc.ID.String(), Key: secondKey, PreviousKey: previous})

		got, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, FileStatusReady, got.FileStatus)
		require.True(t, isFileKey(doc.ID, got.StorageKey))
		assert.NotEqual(t, previous, got.StorageKey)
		assert.Equal(t, []string{got.StorageKey}, objectKeys(t, h, doc.ID), "only the new file is left")
	})

	// A replacement that fails validation must not cost the document the
	// file it already had: the job puts the previous ready file back.
	for name, body := range map[string]*string{"disallowed": &html, "missing": nil} {
		t.Run("a "+name+" replacement restores the previous file", func(t *testing.T) {
			first := readyFile(t, h)
			doc := first

			secondKey := uploadKeyPrefix(doc.ID) + uuid.NewString()
			if body != nil {
				putObject(t, h, secondKey, *body)
			}
			require.NoError(t, h.store.AttachPendingFile(ctx, h.pool, doc.ID, secondKey))

			runValidate(t, h, ValidateUploadArgs{
				DocumentID: doc.ID.String(), Key: secondKey,
				PreviousKey: first.StorageKey, PreviousContentType: first.FileContentType,
			})

			got, err := h.store.GetByID(ctx, h.pool, doc.ID)
			require.NoError(t, err)
			assert.Equal(t, FileStatusReady, got.FileStatus)
			assert.Equal(t, first.StorageKey, got.StorageKey)
			assert.Equal(t, "application/pdf", got.FileContentType)
			assert.Equal(t, []string{first.StorageKey}, objectKeys(t, h, doc.ID), "the previous file is kept, the bad upload is gone")
			assert.Equal(t, pdfBody, readObject(t, h, first.StorageKey))
			assert.Equal(t, [][2]string{{"worker", FileStatusReady}, {"worker", FileStatusReady}}, validationAudit(t, h, doc.ID))
		})
	}

	// River retries a job whose earlier attempt may already have written its
	// verdict: the second run must find nothing to do.
	t.Run("a retried rejection changes nothing", func(t *testing.T) {
		doc, key := pendingUpload(t, h, nil)
		args := ValidateUploadArgs{DocumentID: doc.ID.String(), Key: key}

		runValidate(t, h, args)
		once, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		runValidate(t, h, args)

		twice, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, once, twice)
		assert.Equal(t, FileStatusRejected, twice.FileStatus)
		assert.Equal(t, [][2]string{{"worker", FileStatusRejected}}, validationAudit(t, h, doc.ID), "exactly one audit row")
	})

	t.Run("a retried acceptance changes nothing", func(t *testing.T) {
		doc, key := pendingUpload(t, h, &pdf)
		args := ValidateUploadArgs{DocumentID: doc.ID.String(), Key: key}

		runValidate(t, h, args)
		once, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		runValidate(t, h, args)

		twice, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, once, twice)
		assert.Equal(t, []string{once.StorageKey}, objectKeys(t, h, doc.ID))
		assert.Equal(t, [][2]string{{"worker", FileStatusReady}}, validationAudit(t, h, doc.ID), "exactly one audit row")
	})

	t.Run("a stale key never overwrites the newer upload and leaves no copy", func(t *testing.T) {
		doc, oldKey := pendingUpload(t, h, &pdf)
		newKey := uploadKeyPrefix(doc.ID) + uuid.NewString()
		putObject(t, h, newKey, pdf)
		require.NoError(t, h.store.AttachPendingFile(ctx, h.pool, doc.ID, newKey))

		runValidate(t, h, ValidateUploadArgs{DocumentID: doc.ID.String(), Key: oldKey})

		got, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, newKey, got.StorageKey)
		assert.Equal(t, FileStatusPending, got.FileStatus, "the newer upload still awaits its own job")
		assert.Empty(t, validationAudit(t, h, doc.ID), "a verdict that changed nothing is not audited")
		assert.Equal(t, []string{newKey}, objectKeys(t, h, doc.ID), "the stale copy and the stale upload are deleted")
	})

	t.Run("a document deleted meanwhile is a no-op", func(t *testing.T) {
		doc, key := pendingUpload(t, h, &pdf)
		require.NoError(t, h.store.Delete(ctx, h.pool, doc.ID))

		runValidate(t, h, ValidateUploadArgs{DocumentID: doc.ID.String(), Key: key})

		assert.Empty(t, validationAudit(t, h, doc.ID))
		assert.Empty(t, objectKeys(t, h, doc.ID), "nothing is left behind for a deleted document")
	})
}

// failingStorage is a fake bucket whose reads always fail with a transient
// error, the kind River retries. It records every key Delete is asked for.
type failingStorage struct {
	*fakeStorage
	deleted []string
}

func (f *failingStorage) Download(context.Context, string) (*storage.DownloadOutput, error) {
	return nil, errors.New("storage unavailable")
}

func (f *failingStorage) Delete(ctx context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return f.fakeStorage.Delete(ctx, key)
}

// A job that keeps failing is retried, and the document stays pending while
// it is. On the last attempt River would discard the job and leave the row
// pending forever, so the job resolves it itself, by the rules of a
// rejection, and says so in the log.
func TestValidateUpload_LastAttemptResolvesTheRow(t *testing.T) {
	h, _ := newIntegrationHandler(t)
	ctx := context.Background()

	newFailingWorker := func() (*ValidateUploadWorker, *failingStorage, *bytes.Buffer) {
		fs := &failingStorage{fakeStorage: h.storage.(*fakeStorage)}
		var logs bytes.Buffer
		w := newValidateWorker(h)
		w.storage = fs
		w.logger = slog.New(slog.NewTextHandler(&logs, nil))
		return w, fs, &logs
	}
	attempt := func(args ValidateUploadArgs, n int) *river.Job[ValidateUploadArgs] {
		return &river.Job[ValidateUploadArgs]{
			JobRow: &rivertype.JobRow{Attempt: n, MaxAttempts: 3},
			Args:   args,
		}
	}

	t.Run("an earlier attempt leaves the row pending for the retry", func(t *testing.T) {
		pdf := pdfBody
		doc, key := pendingUpload(t, h, &pdf)
		w, fs, _ := newFailingWorker()

		err := w.Work(ctx, attempt(ValidateUploadArgs{DocumentID: doc.ID.String(), Key: key}, 2))

		require.Error(t, err)
		got, gErr := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, gErr)
		assert.Equal(t, FileStatusPending, got.FileStatus)
		assert.Empty(t, fs.deleted)
		assert.Empty(t, validationAudit(t, h, doc.ID))
	})

	t.Run("the last attempt rejects a first upload", func(t *testing.T) {
		pdf := pdfBody
		doc, key := pendingUpload(t, h, &pdf)
		w, fs, logs := newFailingWorker()

		err := w.Work(ctx, attempt(ValidateUploadArgs{DocumentID: doc.ID.String(), Key: key}, 3))

		require.Error(t, err, "the failure is still reported to River")
		got, gErr := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, gErr)
		assert.Equal(t, FileStatusRejected, got.FileStatus)
		assert.Equal(t, []string{key}, fs.deleted, "the upload object is deleted, best effort")
		assert.Equal(t, [][2]string{{"worker", FileStatusRejected}}, validationAudit(t, h, doc.ID))
		assert.Contains(t, logs.String(), "level=ERROR")
		assert.Contains(t, logs.String(), "document_id="+doc.ID.String())
		assert.Contains(t, logs.String(), "key="+key)
		assert.NotContains(t, logs.String(), "sig=", "no presigned URL is logged")
	})

	t.Run("the last attempt restores a previous file", func(t *testing.T) {
		first := readyFile(t, h)
		secondKey := uploadKeyPrefix(first.ID) + uuid.NewString()
		require.NoError(t, h.store.AttachPendingFile(ctx, h.pool, first.ID, secondKey))
		w, fs, _ := newFailingWorker()

		err := w.Work(ctx, attempt(ValidateUploadArgs{
			DocumentID: first.ID.String(), Key: secondKey,
			PreviousKey: first.StorageKey, PreviousContentType: first.FileContentType,
		}, 3))

		require.Error(t, err)
		got, gErr := h.store.GetByID(ctx, h.pool, first.ID)
		require.NoError(t, gErr)
		assert.Equal(t, FileStatusReady, got.FileStatus)
		assert.Equal(t, first.StorageKey, got.StorageKey)
		assert.Equal(t, first.FileContentType, got.FileContentType)
		assert.Equal(t, []string{secondKey}, fs.deleted, "only the upload is deleted; the previous file is kept")
		assert.Equal(t, [][2]string{{"worker", FileStatusReady}, {"worker", FileStatusReady}}, validationAudit(t, h, first.ID))
	})
}
