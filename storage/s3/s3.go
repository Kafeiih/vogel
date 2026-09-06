// Package s3 provides the S3-compatible implementation of the storage.Storage port.
//
// Works with AWS S3, MinIO, DigitalOcean Spaces, Backblaze B2, and any S3-compatible service.
// For MinIO, set Endpoint and PathStyle=true in Config.
package s3

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/kafeiih/vogel/storage"
)

// Config holds the settings needed to construct an S3-compatible Storage.
type Config struct {
	Bucket        string        // S3 bucket name
	Region        string        // AWS region (default: "us-east-1")
	Endpoint      string        // Custom endpoint for MinIO/S3-compatible (empty = AWS)
	AccessKey     string        // Static access key (empty = use IAM/instance role)
	SecretKey     string        // Static secret key
	PathStyle     bool          // Use path-style access (required for MinIO)
	PresignExpiry time.Duration // Default presigned URL expiry (default: 15m)
}

// Storage implements storage.Storage using AWS SDK v2.
type S3Storage struct {
	client        *s3.Client
	presignClient *s3.PresignClient
	bucket        string
	logger        *slog.Logger
}

// NewS3Storage creates an S3-backed storage adapter.
//
// For AWS S3: set Region and optionally AccessKey/SecretKey (or use IAM roles).
// For MinIO: set Endpoint, AccessKey, SecretKey, and PathStyle=true.
func NewS3Storage(cfg Config, logger *slog.Logger) (*S3Storage, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("storage: bucket is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("storage: logger is required")
	}

	opts := []func(*s3.Options){
		func(o *s3.Options) {
			o.Region = cfg.Region
		},
	}

	// Custom endpoint for MinIO or S3-compatible services.
	if cfg.Endpoint != "" {
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		})
	}

	// Path-style access (required for MinIO, optional for AWS).
	if cfg.PathStyle {
		opts = append(opts, func(o *s3.Options) {
			o.UsePathStyle = true
		})
	}

	// Static credentials for dev/MinIO. In production, omit these to use IAM roles.
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		opts = append(opts, func(o *s3.Options) {
			o.Credentials = credentials.NewStaticCredentialsProvider(
				cfg.AccessKey, cfg.SecretKey, "",
			)
		})
	}

	client := s3.New(s3.Options{}, opts...)
	presignClient := s3.NewPresignClient(client)

	return &S3Storage{
		client:        client,
		presignClient: presignClient,
		bucket:        cfg.Bucket,
		logger:        logger,
	}, nil
}

// Upload stores a file in the configured bucket.
func (s *S3Storage) Upload(ctx context.Context, input *storage.UploadInput) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(input.Key),
		Body:        input.Body,
		ContentType: aws.String(input.ContentType),
	})
	if err != nil {
		return fmt.Errorf("storage: upload %q: %w", input.Key, err)
	}

	s.logger.InfoContext(ctx, "file uploaded",
		"bucket", s.bucket,
		"key", input.Key,
		"content_type", input.ContentType,
	)
	return nil
}

// Download retrieves a file from the configured bucket.
func (s *S3Storage) Download(ctx context.Context, key string) (*storage.DownloadOutput, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("storage: download %q: %w", key, err)
	}

	contentType := ""
	if result.ContentType != nil {
		contentType = *result.ContentType
	}

	return &storage.DownloadOutput{
		Body:        result.Body,
		ContentType: contentType,
	}, nil
}

// Delete removes a file from the configured bucket.
// Deleting a non-existent key is idempotent and returns nil.
func (s *S3Storage) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("storage: delete %q: %w", key, err)
	}

	s.logger.InfoContext(ctx, "file deleted",
		"bucket", s.bucket,
		"key", key,
	)
	return nil
}

// PresignedGetURL generates a temporary download URL.
func (s *S3Storage) PresignedGetURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	req, err := s.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, func(o *s3.PresignOptions) {
		o.Expires = expiry
	})
	if err != nil {
		return "", fmt.Errorf("storage: presign get %q: %w", key, err)
	}
	return req.URL, nil
}

// PresignedPutURL generates a temporary upload URL.
func (s *S3Storage) PresignedPutURL(ctx context.Context, key string, contentType string, expiry time.Duration) (string, error) {
	req, err := s.presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, func(o *s3.PresignOptions) {
		o.Expires = expiry
	})
	if err != nil {
		return "", fmt.Errorf("storage: presign put %q: %w", key, err)
	}
	return req.URL, nil
}
