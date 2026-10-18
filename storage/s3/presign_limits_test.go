package s3

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kafeiih/vogel/storage"
)

// presignCall is a presign method under test, normalized to one signature.
type presignCall func(s *S3Storage, expiry time.Duration) (string, error)

var presignCalls = map[string]presignCall{
	"get": func(s *S3Storage, expiry time.Duration) (string, error) {
		return s.PresignedGetURL(context.Background(), "docs/report.pdf", expiry)
	},
	"put": func(s *S3Storage, expiry time.Duration) (string, error) {
		return s.PresignedPutURL(context.Background(), "docs/report.pdf", "application/pdf", expiry)
	},
}

// A presigned URL is a bearer credential nothing can revoke before it
// expires, so an expiry above the maximum is refused rather than signed.
func TestPresign_ExpiryAboveDefaultMaximum_IsRefused(t *testing.T) {
	s := newPresignTestStorage(t, 0)

	for name, call := range presignCalls {
		t.Run(name, func(t *testing.T) {
			url, err := call(s, DefaultMaxPresignExpiry+time.Second)
			if !errors.Is(err, storage.ErrPresignExpiryTooLong) {
				t.Fatalf("err = %v, want storage.ErrPresignExpiryTooLong", err)
			}
			if url != "" {
				t.Errorf("url = %q, want empty on error", url)
			}
		})
	}
}

func TestPresign_ExpiryAtMaximum_IsSigned(t *testing.T) {
	s := newPresignTestStorage(t, 0)

	for name, call := range presignCalls {
		t.Run(name, func(t *testing.T) {
			url, err := call(s, DefaultMaxPresignExpiry)
			if err != nil {
				t.Fatalf("err = %v, want nil at the maximum", err)
			}
			if got := presignedQuery(t, url).Get("X-Amz-Expires"); got != "3600" {
				t.Errorf("X-Amz-Expires = %q, want %q", got, "3600")
			}
		})
	}
}

func TestPresign_ConfiguredMaximum_IsEnforced(t *testing.T) {
	s, err := NewS3Storage(Config{
		Bucket:           "docs",
		Region:           "us-east-1",
		Endpoint:         "http://localhost:9000",
		AccessKey:        "test-access-key",
		SecretKey:        "test-secret-key",
		PathStyle:        true,
		PresignExpiry:    5 * time.Minute,
		MaxPresignExpiry: 10 * time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}

	for name, call := range presignCalls {
		t.Run(name, func(t *testing.T) {
			if _, err := call(s, 11*time.Minute); !errors.Is(err, storage.ErrPresignExpiryTooLong) {
				t.Errorf("11m: err = %v, want storage.ErrPresignExpiryTooLong", err)
			}
			if _, err := call(s, 10*time.Minute); err != nil {
				t.Errorf("10m: err = %v, want nil", err)
			}
		})
	}
}

func TestNewS3Storage_InvalidPresignBounds_ReturnsError(t *testing.T) {
	for name, cfg := range map[string]Config{
		"default expiry above the maximum":         {Bucket: "docs", PresignExpiry: 20 * time.Minute, MaxPresignExpiry: 10 * time.Minute},
		"default expiry above the default maximum": {Bucket: "docs", PresignExpiry: 2 * time.Hour},
		"negative maximum":                         {Bucket: "docs", MaxPresignExpiry: -time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewS3Storage(cfg, slog.Default()); err == nil {
				t.Error("NewS3Storage: err = nil, want an error")
			}
		})
	}
}

// signedHeaders returns the headers a presigned URL's signature covers.
func signedHeaders(t *testing.T, rawURL string) []string {
	t.Helper()
	return strings.Split(presignedQuery(t, rawURL).Get("X-Amz-SignedHeaders"), ";")
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// WithContentLength signs the exact body size: the store rejects an upload
// whose Content-Length differs, so the URL cannot store an object of any size.
func TestPresignedPutURL_WithContentLength_SignsContentLength(t *testing.T) {
	s := newPresignTestStorage(t, 0)

	url, err := s.PresignedPutURL(context.Background(), "docs/report.pdf", "application/pdf", 0, storage.WithContentLength(1234))
	if err != nil {
		t.Fatalf("PresignedPutURL: %v", err)
	}
	if headers := signedHeaders(t, url); !contains(headers, "content-length") {
		t.Errorf("X-Amz-SignedHeaders = %v, want content-length signed", headers)
	}
}

// Without the option the URL keeps its previous shape.
func TestPresignedPutURL_WithoutContentLength_DoesNotSignIt(t *testing.T) {
	s := newPresignTestStorage(t, 0)

	url, err := s.PresignedPutURL(context.Background(), "docs/report.pdf", "application/pdf", 0)
	if err != nil {
		t.Fatalf("PresignedPutURL: %v", err)
	}
	if headers := signedHeaders(t, url); contains(headers, "content-length") {
		t.Errorf("X-Amz-SignedHeaders = %v, want content-length not signed", headers)
	}
}

func TestPresignedPutURL_NegativeContentLength_ReturnsError(t *testing.T) {
	s := newPresignTestStorage(t, 0)

	url, err := s.PresignedPutURL(context.Background(), "docs/report.pdf", "application/pdf", 0, storage.WithContentLength(-1))
	if !errors.Is(err, storage.ErrInvalidContentLength) {
		t.Fatalf("err = %v, want storage.ErrInvalidContentLength", err)
	}
	if url != "" {
		t.Errorf("url = %q, want empty on error", url)
	}
}
