package s3

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kafeiih/vogel/storage"
)

// newStorageAgainst builds an adapter pointed at srv.
func newStorageAgainst(t *testing.T, srv *httptest.Server) *S3Storage {
	t.Helper()
	s, err := NewS3Storage(Config{
		Bucket: "docs", Region: "us-east-1", Endpoint: srv.URL,
		AccessKey: "k", SecretKey: "s", PathStyle: true,
	}, slog.Default())
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}
	return s
}

// A missing object is reported as storage.ErrNotFound, so a caller can tell
// it from a transient failure without importing the AWS SDK.
func TestDownload_MissingObject_IsErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))
	}))
	defer srv.Close()

	out, err := newStorageAgainst(t, srv).Download(context.Background(), "docs/missing.pdf")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want storage.ErrNotFound", err)
	}
	if out != nil {
		t.Errorf("out = %+v, want nil on error", out)
	}
}

// Other failures keep their own error, so they stay retryable.
func TestDownload_ServerError_IsNotErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>denied</Message></Error>`))
	}))
	defer srv.Close()

	_, err := newStorageAgainst(t, srv).Download(context.Background(), "docs/x.pdf")
	if err == nil || errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want a non-ErrNotFound error", err)
	}
}
