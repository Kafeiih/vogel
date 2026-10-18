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
	valid := Config{Bucket: "docs", Region: "us-east-1", Endpoint: "http://localhost:9000", AccessKey: "k", SecretKey: "s", PathStyle: true}
	with := func(mutate func(*Config)) Config {
		cfg := valid
		mutate(&cfg)
		return cfg
	}

	for name, tc := range map[string]struct {
		cfg  Config
		want string
	}{
		"default expiry above the maximum": {
			with(func(c *Config) { c.PresignExpiry, c.MaxPresignExpiry = 20*time.Minute, 10*time.Minute }),
			"presign expiry 20m0s exceeds the maximum 10m0s",
		},
		"default expiry above the default maximum": {
			with(func(c *Config) { c.PresignExpiry = 2 * time.Hour }),
			"presign expiry 2h0m0s exceeds the maximum 1h0m0s",
		},
		"implicit 15m default above a smaller maximum": {
			with(func(c *Config) { c.MaxPresignExpiry = 5 * time.Minute }),
			"presign expiry 15m0s exceeds the maximum 5m0s",
		},
		"negative maximum": {
			with(func(c *Config) { c.MaxPresignExpiry = -time.Minute }),
			"max presign expiry must not be negative",
		},
		"maximum above what SigV4 can sign": {
			with(func(c *Config) { c.MaxPresignExpiry = 8 * 24 * time.Hour }),
			"max presign expiry 192h0m0s exceeds the 168h0m0s SigV4 allows",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewS3Storage(tc.cfg, slog.Default())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("NewS3Storage: err = %v, want one containing %q", err, tc.want)
			}
		})
	}

	// The valid base config builds: the cases above fail only on the bound.
	if _, err := NewS3Storage(valid, slog.Default()); err != nil {
		t.Fatalf("NewS3Storage(valid): %v", err)
	}
}

// SigV4's own ceiling is accepted as a maximum.
func TestNewS3Storage_MaximumAtSigV4Limit_IsAccepted(t *testing.T) {
	_, err := NewS3Storage(Config{Bucket: "docs", Region: "us-east-1", MaxPresignExpiry: 7 * 24 * time.Hour}, slog.Default())
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
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
	for _, h := range []string{"content-length", "content-type"} {
		if headers := signedHeaders(t, url); !contains(headers, h) {
			t.Errorf("X-Amz-SignedHeaders = %v, want %s signed", headers, h)
		}
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

// A size the SDK cannot sign is refused: the AWS SDK drops a zero
// Content-Length from the signature, and with it Content-Type, which would
// leave a URL weaker than one signed without the option.
func TestPresignedPutURL_NonPositiveContentLength_ReturnsError(t *testing.T) {
	s := newPresignTestStorage(t, 0)

	for _, n := range []int64{0, -1} {
		url, err := s.PresignedPutURL(context.Background(), "docs/report.pdf", "application/pdf", 0, storage.WithContentLength(n))
		if !errors.Is(err, storage.ErrInvalidContentLength) {
			t.Errorf("n=%d: err = %v, want storage.ErrInvalidContentLength", n, err)
		}
		if url != "" {
			t.Errorf("n=%d: url = %q, want empty on error", n, url)
		}
	}
}

// One byte, the smallest accepted size, is signed with the type.
func TestPresignedPutURL_ContentLengthOne_IsSigned(t *testing.T) {
	s := newPresignTestStorage(t, 0)

	url, err := s.PresignedPutURL(context.Background(), "docs/report.pdf", "application/pdf", 0, storage.WithContentLength(1))
	if err != nil {
		t.Fatalf("PresignedPutURL: %v", err)
	}
	for _, h := range []string{"content-length", "content-type"} {
		if headers := signedHeaders(t, url); !contains(headers, h) {
			t.Errorf("X-Amz-SignedHeaders = %v, want %s signed", headers, h)
		}
	}
}
