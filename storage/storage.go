// Package storage defines the contracts for object storage operations.
//
// The Storage interface abstracts file upload, download, delete, and presigned URL generation.
// This package is dependency-free: it declares the port only. Concrete adapters (e.g. the
// S3-compatible implementation in storage/s3) live in subpackages so that a consumer importing
// only this port does not pull in an SDK it does not need.
//
// Usage from application layer:
//
//	// Upload a file:
//	err := storage.Upload(ctx, &storage.UploadInput{
//	    Key:         "docs/report.pdf",
//	    Body:        file,
//	    ContentType: "application/pdf",
//	})
//
//	// Generate a presigned download URL for frontend:
//	url, err := storage.PresignedGetURL(ctx, "docs/report.pdf", 15*time.Minute)
package storage

import (
	"context"
	"io"
	"time"
)

// UploadInput contains the data needed to upload a file.
type UploadInput struct {
	Key         string    // Object key (path) in the bucket, e.g. "docs/report.pdf"
	Body        io.Reader // File content
	ContentType string    // MIME type, e.g. "application/pdf"
}

// DownloadOutput contains the result of a file download.
type DownloadOutput struct {
	Body        io.ReadCloser // File content — caller MUST close this
	ContentType string        // MIME type of the stored object
}

// Storage abstracts object storage operations.
//
// Two primary usage patterns:
//   - Direct: Backend uploads/downloads files via Upload/Download methods.
//   - Presigned: Frontend uploads/downloads directly via presigned URLs (no backend proxy).
type Storage interface {
	// Upload stores a file in the configured bucket.
	Upload(ctx context.Context, input *UploadInput) error

	// Download retrieves a file from the configured bucket.
	// Caller MUST close the returned Body when done.
	Download(ctx context.Context, key string) (*DownloadOutput, error)

	// Delete removes a file from the configured bucket.
	// Deleting a non-existent key is idempotent and returns nil.
	Delete(ctx context.Context, key string) error

	// PresignedGetURL generates a temporary URL for downloading a file.
	// The URL is valid for the specified duration and requires no authentication.
	PresignedGetURL(ctx context.Context, key string, expiry time.Duration) (string, error)

	// PresignedPutURL generates a temporary URL for uploading a file.
	// The URL is valid for the specified duration and accepts the specified content type.
	PresignedPutURL(ctx context.Context, key string, contentType string, expiry time.Duration) (string, error)
}
