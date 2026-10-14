package s3

import (
	"context"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/kafeiih/vogel/storage"
)

// newPresignTestStorage builds an adapter with static credentials so the
// presigner can sign locally. Presigning never contacts the endpoint: the
// SigV4 signature is computed in-process, so these tests need no network and
// no running MinIO.
func newPresignTestStorage(t *testing.T, presignExpiry time.Duration) *S3Storage {
	t.Helper()

	s, err := NewS3Storage(Config{
		Bucket:        "docs",
		Region:        "us-east-1",
		Endpoint:      "http://localhost:9000",
		AccessKey:     "test-access-key",
		SecretKey:     "test-secret-key",
		PathStyle:     true,
		PresignExpiry: presignExpiry,
	}, slog.Default())
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}
	return s
}

// presignedQuery parses a presigned URL and returns its query parameters.
func presignedQuery(t *testing.T, rawURL string) url.Values {
	t.Helper()

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", rawURL, err)
	}
	return u.Query()
}

func TestPresignedGetURL_Expiry(t *testing.T) {
	tests := []struct {
		name          string
		presignExpiry time.Duration
		expiry        time.Duration
		wantExpires   string
	}{
		{
			name:          "zero expiry uses the adapter default of 15m when the config is unset",
			presignExpiry: 0,
			expiry:        0,
			wantExpires:   "900",
		},
		{
			name:          "zero expiry uses Config.PresignExpiry when set",
			presignExpiry: 30 * time.Minute,
			expiry:        0,
			wantExpires:   "1800",
		},
		{
			name:          "negative expiry uses Config.PresignExpiry",
			presignExpiry: 30 * time.Minute,
			expiry:        -time.Minute,
			wantExpires:   "1800",
		},
		{
			name:          "explicit expiry wins over Config.PresignExpiry",
			presignExpiry: 30 * time.Minute,
			expiry:        5 * time.Minute,
			wantExpires:   "300",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newPresignTestStorage(t, tt.presignExpiry)

			rawURL, err := s.PresignedGetURL(context.Background(), "docs/report.pdf", tt.expiry)
			if err != nil {
				t.Fatalf("PresignedGetURL: %v", err)
			}

			if got := presignedQuery(t, rawURL).Get("X-Amz-Expires"); got != tt.wantExpires {
				t.Errorf("X-Amz-Expires = %q, want %q", got, tt.wantExpires)
			}
		})
	}
}

func TestPresignedPutURL_Expiry(t *testing.T) {
	tests := []struct {
		name          string
		presignExpiry time.Duration
		expiry        time.Duration
		wantExpires   string
	}{
		{
			name:          "zero expiry uses the adapter default of 15m when the config is unset",
			presignExpiry: 0,
			expiry:        0,
			wantExpires:   "900",
		},
		{
			name:          "zero expiry uses Config.PresignExpiry when set",
			presignExpiry: 30 * time.Minute,
			expiry:        0,
			wantExpires:   "1800",
		},
		{
			name:          "explicit expiry wins over Config.PresignExpiry",
			presignExpiry: 30 * time.Minute,
			expiry:        5 * time.Minute,
			wantExpires:   "300",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newPresignTestStorage(t, tt.presignExpiry)

			rawURL, err := s.PresignedPutURL(context.Background(), "docs/report.pdf", "application/pdf", tt.expiry)
			if err != nil {
				t.Fatalf("PresignedPutURL: %v", err)
			}

			if got := presignedQuery(t, rawURL).Get("X-Amz-Expires"); got != tt.wantExpires {
				t.Errorf("X-Amz-Expires = %q, want %q", got, tt.wantExpires)
			}
		})
	}
}

func TestPresignedGetURL_ResponseHeaders(t *testing.T) {
	tests := []struct {
		name            string
		opts            []storage.PresignGetOption
		wantDisposition string
		wantContentType string
	}{
		{
			name:            "no options default to an octet-stream attachment so uploads never render on the bucket origin",
			opts:            nil,
			wantDisposition: "attachment",
			wantContentType: "application/octet-stream",
		},
		{
			name:            "AsAttachment carries the sanitized filename",
			opts:            []storage.PresignGetOption{storage.AsAttachment("report\r\n.pdf")},
			wantDisposition: `attachment; filename="report.pdf"`,
			wantContentType: "application/octet-stream",
		},
		{
			name:            "WithContentDisposition inline is an explicit opt-in",
			opts:            []storage.PresignGetOption{storage.WithContentDisposition("inline")},
			wantDisposition: "inline",
			wantContentType: "application/octet-stream",
		},
		{
			name:            "WithResponseContentType maps to response-content-type",
			opts:            []storage.PresignGetOption{storage.WithResponseContentType("image/png")},
			wantDisposition: "attachment",
			wantContentType: "image/png",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newPresignTestStorage(t, 0)

			rawURL, err := s.PresignedGetURL(context.Background(), "docs/report.pdf", time.Minute, tt.opts...)
			if err != nil {
				t.Fatalf("PresignedGetURL: %v", err)
			}

			q := presignedQuery(t, rawURL)
			if got := q.Get("response-content-disposition"); got != tt.wantDisposition {
				t.Errorf("response-content-disposition = %q, want %q", got, tt.wantDisposition)
			}
			if got := q.Get("response-content-type"); got != tt.wantContentType {
				t.Errorf("response-content-type = %q, want %q", got, tt.wantContentType)
			}
		})
	}
}
