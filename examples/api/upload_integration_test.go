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
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/auth"
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
		assert.Equal(t, ValidateUploadArgs{DocumentID: doc.ID.String(), Key: key, PreviousKey: fileKey}, args)
	})
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

	err := newValidateWorker(h).Work(ctx, &river.Job[ValidateUploadArgs]{
		Args: ValidateUploadArgs{DocumentID: doc.ID.String(), Key: upload.Data.Key},
	})
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
