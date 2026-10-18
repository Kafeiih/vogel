//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/storage"
)

// callHandler sends body to handler as user-alice with the chi {id}
// parameter set to id, and returns the recorder.
func callHandler(handler http.HandlerFunc, method, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/documents/"+id, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), &auth.Principal{UserID: "user-alice"}))
	req = withURLParam(req, "id", id)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

type uploadURLEnvelope struct {
	Data struct {
		URL       string            `json:"url"`
		Key       string            `json:"key"`
		Method    string            `json:"method"`
		Headers   map[string]string `json:"headers"`
		ExpiresAt time.Time         `json:"expires_at"`
	} `json:"data"`
}

func TestDocumentHandlerUploadURL(t *testing.T) {
	h, _ := newIntegrationHandler(t)
	ctx := context.Background()

	t.Run("missing document is 404", func(t *testing.T) {
		rec := callHandler(h.UploadURL, http.MethodPost, uuid.NewString(), `{"content_type":"application/pdf","size":10}`)
		assert.Equal(t, http.StatusNotFound, rec.Code, "body %s", rec.Body.String())
	})

	t.Run("signs a PUT for the requested type and size", func(t *testing.T) {
		doc := createTestDocument(t, h)
		before := time.Now()

		rec := callHandler(h.UploadURL, http.MethodPost, doc.ID.String(), `{"content_type":"application/pdf","size":1234}`)

		require.Equal(t, http.StatusOK, rec.Code, "body %s", rec.Body.String())
		var resp uploadURLEnvelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		got := resp.Data
		assert.Equal(t, http.MethodPut, got.Method)
		assert.Equal(t, map[string]string{"Content-Type": "application/pdf"}, got.Headers)
		require.True(t, strings.HasPrefix(got.Key, uploadKeyPrefix(doc.ID)), "key %q is under the document's upload prefix", got.Key)
		_, err := uuid.Parse(strings.TrimPrefix(got.Key, uploadKeyPrefix(doc.ID)))
		assert.NoError(t, err, "each upload gets its own UUID key")
		assert.WithinDuration(t, before.Add(uploadURLExpiry), got.ExpiresAt, 2*time.Second)

		u, err := url.Parse(got.URL)
		require.NoError(t, err)
		assert.Equal(t, fakeStoragePath+got.Key, u.Path)
		q := u.Query()
		assert.Equal(t, "application/pdf", q.Get("content_type"), "the URL signs the type")
		assert.Equal(t, "1234", q.Get("content_length"), "the URL signs the exact size")
		expires, err := strconv.ParseInt(q.Get("expires"), 10, 64)
		require.NoError(t, err)
		assert.LessOrEqual(t, expires, time.Now().Add(uploadURLExpiry).Unix(), "the URL lives at most uploadURLExpiry")

		stored, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, FileStatusNone, stored.FileStatus, "signing persists nothing")
		assert.Empty(t, stored.StorageKey)
	})
}

func TestDocumentHandlerAttachFile(t *testing.T) {
	h, _ := newIntegrationHandler(t)
	ctx := context.Background()

	t.Run("missing document is 404", func(t *testing.T) {
		id := uuid.New()
		rec := callHandler(h.AttachFile, http.MethodPost, id.String(), `{"key":"`+uploadKeyPrefix(id)+uuid.NewString()+`"}`)
		assert.Equal(t, http.StatusNotFound, rec.Code, "body %s", rec.Body.String())
	})

	t.Run("marks the file pending, enqueues validation and audits", func(t *testing.T) {
		doc := createTestDocument(t, h)
		key := uploadKeyPrefix(doc.ID) + uuid.NewString()

		rec := callHandler(h.AttachFile, http.MethodPost, doc.ID.String(), `{"key":"`+key+`"}`)

		require.Equal(t, http.StatusAccepted, rec.Code, "body %s", rec.Body.String())
		var resp struct {
			Data Document `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, key, resp.Data.StorageKey)
		assert.Equal(t, FileStatusPending, resp.Data.FileStatus)

		stored, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, key, stored.StorageKey)
		assert.Equal(t, FileStatusPending, stored.FileStatus)
		assert.Empty(t, stored.FileContentType)

		var args ValidateUploadArgs
		require.NoError(t, h.pool.QueryRow(ctx,
			`SELECT args FROM river.river_job WHERE kind = 'validate_upload' AND args->>'document_id' = $1`,
			doc.ID.String()).Scan(&args), "a validate_upload job was enqueued")
		assert.Equal(t, ValidateUploadArgs{DocumentID: doc.ID.String(), Key: key}, args)

		var source, beforeStatus, afterStatus, afterKey string
		require.NoError(t, h.pool.QueryRow(ctx, `
			SELECT source, before_snapshot->>'file_status', after_snapshot->>'file_status', after_snapshot->>'storage_key'
			FROM audit_log WHERE resource_id = $1 AND operation_name = 'document_attach_file'`,
			doc.ID.String()).Scan(&source, &beforeStatus, &afterStatus, &afterKey))
		assert.Equal(t, "http", source)
		assert.Equal(t, FileStatusNone, beforeStatus)
		assert.Equal(t, FileStatusPending, afterStatus)
		assert.Equal(t, key, afterKey)
	})

	// Replacing a ready file hands its final key to the job, which deletes
	// it once the row no longer points at it: the confirm itself detaches it.
	t.Run("replacing a ready file passes its key to the job", func(t *testing.T) {
		doc := createTestDocument(t, h)
		uploadKey := uploadKeyPrefix(doc.ID) + uuid.NewString()
		fileKey := fileKeyPrefix(doc.ID) + uuid.NewString()
		require.NoError(t, h.store.AttachPendingFile(ctx, h.pool, doc.ID, uploadKey))
		_, err := h.store.ResolveUpload(ctx, h.pool, doc.ID, uploadKey, fileKey, FileStatusReady, "application/pdf")
		require.NoError(t, err)
		key := uploadKeyPrefix(doc.ID) + uuid.NewString()

		rec := callHandler(h.AttachFile, http.MethodPost, doc.ID.String(), `{"key":"`+key+`"}`)

		require.Equal(t, http.StatusAccepted, rec.Code, "body %s", rec.Body.String())
		var args ValidateUploadArgs
		require.NoError(t, h.pool.QueryRow(ctx,
			`SELECT args FROM river.river_job WHERE kind = 'validate_upload' AND args->>'document_id' = $1`,
			doc.ID.String()).Scan(&args))
		assert.Equal(t, ValidateUploadArgs{
			DocumentID: doc.ID.String(), Key: key,
			PreviousKey: fileKey, PreviousContentType: "application/pdf",
		}, args)
	})

	// A client that retries its confirm with a key the job already consumed
	// must not lose the file: the second job finds the upload gone, and puts
	// the validated file back instead of deleting it.
	t.Run("confirming the same key twice keeps the validated file", func(t *testing.T) {
		doc := createTestDocument(t, h)
		key := uploadKeyPrefix(doc.ID) + uuid.NewString()
		putObject(t, h, key, pdfBody)

		for i := 0; i < 2; i++ {
			rec := callHandler(h.AttachFile, http.MethodPost, doc.ID.String(), `{"key":"`+key+`"}`)
			require.Equal(t, http.StatusAccepted, rec.Code, "confirm %d: %s", i+1, rec.Body.String())
			runValidate(t, h, latestValidateArgs(t, h, doc.ID))
		}

		got, err := h.store.GetByID(ctx, h.pool, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, FileStatusReady, got.FileStatus)
		require.True(t, isFileKey(doc.ID, got.StorageKey))
		assert.Equal(t, "application/pdf", got.FileContentType)

		rec := callHandler(h.DownloadURL, http.MethodGet, doc.ID.String(), "")
		require.Equal(t, http.StatusOK, rec.Code, "body %s", rec.Body.String())
		var resp struct {
			Data map[string]string `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		get := sendSigned(t, h.storage.(*fakeStorage), http.MethodGet, resp.Data["url"], "", "")
		require.Equal(t, http.StatusOK, get.Code)
		assert.Equal(t, pdfBody, get.Body.String(), "the original final file is still there")
	})
}

// latestValidateArgs returns the args of the most recent validate_upload job
// enqueued for docID.
func latestValidateArgs(t *testing.T, h *DocumentHandler, docID uuid.UUID) ValidateUploadArgs {
	t.Helper()
	var args ValidateUploadArgs
	require.NoError(t, h.pool.QueryRow(context.Background(), `
		SELECT args FROM river.river_job
		WHERE kind = 'validate_upload' AND args->>'document_id' = $1
		ORDER BY id DESC LIMIT 1`, docID.String()).Scan(&args))
	return args
}

func TestDocumentHandlerDownloadURL(t *testing.T) {
	h, _ := newIntegrationHandler(t)
	ctx := context.Background()

	for _, status := range []string{FileStatusNone, FileStatusPending, FileStatusRejected} {
		t.Run(status+" file is 409", func(t *testing.T) {
			doc := createTestDocument(t, h)
			if status != FileStatusNone {
				key := uploadKeyPrefix(doc.ID) + uuid.NewString()
				require.NoError(t, h.store.AttachPendingFile(ctx, h.pool, doc.ID, key))
				if status == FileStatusRejected {
					_, err := h.store.ResolveUpload(ctx, h.pool, doc.ID, key, key, FileStatusRejected, "")
					require.NoError(t, err)
				}
			}

			rec := callHandler(h.DownloadURL, http.MethodGet, doc.ID.String(), "")

			assert.Equal(t, http.StatusConflict, rec.Code, "body %s", rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "fake-storage", "no URL is handed out")
		})
	}

	// Migration 002 marks a row that already had a key as ready with a NULL
	// file_content_type. Its download falls back to the port's default type,
	// application/octet-stream, never to a type nobody verified.
	t.Run("legacy ready file without a type is served as octet-stream", func(t *testing.T) {
		doc := createTestDocument(t, h)
		legacyKey := "documents/" + doc.ID.String()
		_, err := h.pool.Exec(ctx, `UPDATE documents
			SET storage_key = $1, file_status = 'ready', file_content_type = NULL WHERE id = $2`, legacyKey, doc.ID)
		require.NoError(t, err)

		rec := callHandler(h.DownloadURL, http.MethodGet, doc.ID.String(), "")

		require.Equal(t, http.StatusOK, rec.Code, "body %s", rec.Body.String())
		var resp struct {
			Data map[string]string `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		u, err := url.Parse(resp.Data["url"])
		require.NoError(t, err)
		assert.Equal(t, fakeStoragePath+legacyKey, u.Path)
		assert.Equal(t, storage.DefaultResponseContentType, u.Query().Get("response_content_type"))
		assert.Equal(t, "application/octet-stream", storage.DefaultResponseContentType)
	})

	t.Run("ready file is served with its sniffed type", func(t *testing.T) {
		doc := createTestDocument(t, h)
		uploadKey := uploadKeyPrefix(doc.ID) + uuid.NewString()
		key := fileKeyPrefix(doc.ID) + uuid.NewString()
		require.NoError(t, h.store.AttachPendingFile(ctx, h.pool, doc.ID, uploadKey))
		_, err := h.store.ResolveUpload(ctx, h.pool, doc.ID, uploadKey, key, FileStatusReady, "image/png")
		require.NoError(t, err)

		rec := callHandler(h.DownloadURL, http.MethodGet, doc.ID.String(), "")

		require.Equal(t, http.StatusOK, rec.Code, "body %s", rec.Body.String())
		var resp struct {
			Data map[string]string `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		u, err := url.Parse(resp.Data["url"])
		require.NoError(t, err)
		assert.Equal(t, fakeStoragePath+key, u.Path)
		assert.Equal(t, "image/png", u.Query().Get("response_content_type"))
		assert.Equal(t, "attachment", u.Query().Get("disposition"))
		expires, err := strconv.ParseInt(u.Query().Get("expires"), 10, 64)
		require.NoError(t, err)
		assert.LessOrEqual(t, expires, time.Now().Add(downloadURLExpiry).Unix())
	})
}

// TestPresignedUploadFlow runs the whole flow a client follows: ask for an
// upload URL, PUT the bytes straight to the (fake) bucket, confirm, let
// validate_upload decide, then download through a presigned GET. The API
// never handles the file's bytes, and no presigned URL reaches a log line
// or an audit row.
func TestPresignedUploadFlow(t *testing.T) {
	h, _ := newIntegrationHandler(t)
	ctx := context.Background()
	var logs bytes.Buffer
	h.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fake, ok := h.storage.(*fakeStorage)
	require.True(t, ok)
	doc := createTestDocument(t, h)

	rec := callHandler(h.UploadURL, http.MethodPost, doc.ID.String(),
		`{"content_type":"application/pdf","size":`+strconv.Itoa(len(pdfBody))+`}`)
	require.Equal(t, http.StatusOK, rec.Code, "body %s", rec.Body.String())
	var upload uploadURLEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &upload))

	put := sendSigned(t, fake, upload.Data.Method, upload.Data.URL, upload.Data.Headers["Content-Type"], pdfBody)
	require.Equal(t, http.StatusOK, put.Code, "PUT to the bucket: %s", put.Body.String())

	rec = callHandler(h.AttachFile, http.MethodPost, doc.ID.String(), `{"key":"`+upload.Data.Key+`"}`)
	require.Equal(t, http.StatusAccepted, rec.Code, "body %s", rec.Body.String())

	err := newValidateWorker(h).Work(ctx, validateJob(ValidateUploadArgs{DocumentID: doc.ID.String(), Key: upload.Data.Key}))
	require.NoError(t, err)

	rec = callHandler(h.DownloadURL, http.MethodGet, doc.ID.String(), "")
	require.Equal(t, http.StatusOK, rec.Code, "body %s", rec.Body.String())
	var download struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &download))

	get := sendSigned(t, fake, http.MethodGet, download.Data["url"], "", "")
	require.Equal(t, http.StatusOK, get.Code)
	assert.Equal(t, pdfBody, get.Body.String())
	assert.Equal(t, "application/pdf", get.Header().Get("Content-Type"))

	// The upload URL is still within its 5 minutes. Reusing it after the
	// file was validated must not replace the validated bytes: the job moved
	// them to a final key no URL can PUT to.
	swapped := "<html><script>x</script>" + strings.Repeat(" ", len(pdfBody)-len("<html><script>x</script>"))
	reuse := sendSigned(t, fake, upload.Data.Method, upload.Data.URL, upload.Data.Headers["Content-Type"], swapped)
	require.Equal(t, http.StatusOK, reuse.Code, "the bucket accepts the reused URL: %s", reuse.Body.String())
	get = sendSigned(t, fake, http.MethodGet, download.Data["url"], "", "")
	require.Equal(t, http.StatusOK, get.Code)
	assert.Equal(t, pdfBody, get.Body.String(), "the validated file is unchanged")

	for _, raw := range []string{upload.Data.URL, download.Data["url"]} {
		sig, err := url.Parse(raw)
		require.NoError(t, err)
		signature := sig.Query().Get("sig")
		require.NotEmpty(t, signature)
		assert.NotContains(t, logs.String(), signature, "a presigned URL reached the log")

		var audited int
		require.NoError(t, h.pool.QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE audit_log::text LIKE '%' || $1 || '%'`, signature).Scan(&audited))
		assert.Zero(t, audited, "a presigned URL reached the audit log")
	}
}

// AttachFile decides what to hand the job (PreviousKey, PreviousContentType)
// from the row as it is once the confirm holds it, not from a snapshot read
// before another writer commits. Here a concurrent transaction holds the
// row lock and makes the file ready while the confirm is already running.
// Reading the row with a plain SELECT would see the old state (no file),
// hand the job no previous file, and leak the final file the other writer
// committed. Reading it FOR UPDATE waits for that commit and sees it.
func TestDocumentHandlerAttachFile_DecidesFromTheLockedRow(t *testing.T) {
	h, _ := newIntegrationHandler(t)
	ctx := context.Background()
	doc := createTestDocument(t, h)
	finalKey := fileKeyPrefix(doc.ID) + uuid.NewString()

	holder, err := h.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(ctx) }()
	_, err = holder.Exec(ctx, `SELECT 1 FROM documents WHERE id = $1 FOR UPDATE`, doc.ID)
	require.NoError(t, err)
	_, err = holder.Exec(ctx, `UPDATE documents
		SET storage_key = $1, file_status = 'ready', file_content_type = 'application/pdf' WHERE id = $2`,
		finalKey, doc.ID)
	require.NoError(t, err)

	key := uploadKeyPrefix(doc.ID) + uuid.NewString()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- callHandler(h.AttachFile, http.MethodPost, doc.ID.String(), `{"key":"`+key+`"}`)
	}()

	// Give the confirm time to reach its read of the row before the other
	// writer commits.
	time.Sleep(300 * time.Millisecond)
	require.NoError(t, holder.Commit(ctx))

	rec := <-done
	require.Equal(t, http.StatusAccepted, rec.Code, "body %s", rec.Body.String())
	assert.Equal(t, ValidateUploadArgs{
		DocumentID: doc.ID.String(), Key: key,
		PreviousKey: finalKey, PreviousContentType: "application/pdf",
	}, latestValidateArgs(t, h, doc.ID))
}

// validateJobCount is how many validate_upload jobs were enqueued for docID.
func validateJobCount(t *testing.T, h *DocumentHandler, docID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river.river_job WHERE kind = 'validate_upload' AND args->>'document_id' = $1`,
		docID.String()).Scan(&n))
	return n
}

// A second confirm while the first upload is still pending would hand its
// job no previous file (the row holds an upload key, not a ready file), so
// a ready file the first job is about to restore or replace could be lost.
// The confirm refuses instead: one upload is validated at a time.
func TestDocumentHandlerAttachFile_RefusesWhilePending(t *testing.T) {
	h, _ := newIntegrationHandler(t)
	ctx := context.Background()
	doc := createTestDocument(t, h)
	keyA := uploadKeyPrefix(doc.ID) + uuid.NewString()
	keyB := uploadKeyPrefix(doc.ID) + uuid.NewString()
	putObject(t, h, keyA, pdfBody)
	putObject(t, h, keyB, pdfBody)

	rec := callHandler(h.AttachFile, http.MethodPost, doc.ID.String(), `{"key":"`+keyA+`"}`)
	require.Equal(t, http.StatusAccepted, rec.Code, "body %s", rec.Body.String())
	pending, err := h.store.GetByID(ctx, h.pool, doc.ID)
	require.NoError(t, err)

	rec = callHandler(h.AttachFile, http.MethodPost, doc.ID.String(), `{"key":"`+keyB+`"}`)

	assert.Equal(t, http.StatusConflict, rec.Code, "body %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "CONFLICT")
	assert.Contains(t, rec.Body.String(), "a previous upload is still being validated")
	got, err := h.store.GetByID(ctx, h.pool, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, pending, got, "the row is unchanged")
	assert.Equal(t, 1, validateJobCount(t, h, doc.ID), "nothing more is enqueued")

	runValidate(t, h, latestValidateArgs(t, h, doc.ID))

	rec = callHandler(h.AttachFile, http.MethodPost, doc.ID.String(), `{"key":"`+keyB+`"}`)
	assert.Equal(t, http.StatusAccepted, rec.Code, "once resolved, a new confirm is accepted: %s", rec.Body.String())
	assert.Equal(t, 2, validateJobCount(t, h, doc.ID))
}
