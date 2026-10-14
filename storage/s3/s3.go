// Package s3 provides the S3-compatible implementation of the storage.Storage port.
//
// Works with AWS S3, MinIO, DigitalOcean Spaces, Backblaze B2, and any S3-compatible service.
// For MinIO, set Endpoint and PathStyle=true in Config.
package s3

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
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
	PresignExpiry time.Duration // Default presigned URL expiry when a call passes expiry <= 0 (default: 15m)

	// ResponseHeaderTimeout bounds the phases that reveal a stalled S3
	// endpoint, so an Upload or Download whose context has no deadline cannot
	// hang forever: the wait for the response headers once the request
	// (including an upload body) has been written, and, when smaller than the
	// SDK defaults (30s dial, 10s TLS handshake), connection setup as well.
	// It does not bound the transfer of a response body: a long, healthy
	// Download keeps streaming for as long as the caller reads it, so callers
	// bound whole transfers with their context. Zero means
	// DefaultResponseHeaderTimeout; negative is a construction error.
	// Presigning does no I/O and is unaffected.
	ResponseHeaderTimeout time.Duration
}

// DefaultResponseHeaderTimeout is the response-header bound used when
// Config.ResponseHeaderTimeout is zero.
const DefaultResponseHeaderTimeout = 60 * time.Second

// DefaultPresignExpiry is the presigned URL lifetime used when
// Config.PresignExpiry is zero and a call passes a non-positive expiry.
const DefaultPresignExpiry = 15 * time.Minute

// Storage implements storage.Storage using AWS SDK v2.
type S3Storage struct {
	client        *s3.Client
	presignClient *s3.PresignClient
	bucket        string
	presignExpiry time.Duration
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
	if cfg.ResponseHeaderTimeout < 0 {
		return nil, fmt.Errorf("storage: response header timeout must not be negative")
	}

	headerTimeout := cfg.ResponseHeaderTimeout
	if headerTimeout == 0 {
		headerTimeout = DefaultResponseHeaderTimeout
	}

	opts := []func(*s3.Options){
		func(o *s3.Options) {
			o.Region = cfg.Region
			o.HTTPClient = newHTTPClient(headerTimeout)
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

	presignExpiry := cfg.PresignExpiry
	if presignExpiry <= 0 {
		presignExpiry = DefaultPresignExpiry
	}

	return &S3Storage{
		client:        client,
		presignClient: presignClient,
		bucket:        cfg.Bucket,
		presignExpiry: presignExpiry,
		logger:        logger,
	}, nil
}

// newHTTPClient returns a BuildableClient that keeps the SDK's transport
// defaults and bounds only the phases that indicate a stalled endpoint: the
// wait for response headers, and the dial and TLS handshake when bound is
// tighter than the SDK defaults. It sets no http.Client.Timeout on purpose:
// that would also cap body streaming, and the SDK retryer treats it as
// retryable, so a slow but healthy Download would fail and be re-sent.
func newHTTPClient(bound time.Duration) *awshttp.BuildableClient {
	return awshttp.NewBuildableClient().
		WithDialerOptions(func(d *net.Dialer) {
			d.Timeout = min(d.Timeout, bound)
		}).
		WithTransportOptions(func(tr *http.Transport) {
			tr.TLSHandshakeTimeout = min(tr.TLSHandshakeTimeout, bound)
			tr.ResponseHeaderTimeout = bound
		})
}

// effectiveExpiry returns expiry, or the configured default when expiry is
// not positive, so a caller can pass 0 to mean "whatever the adapter was
// configured with".
func (s *S3Storage) effectiveExpiry(expiry time.Duration) time.Duration {
	if expiry <= 0 {
		return s.presignExpiry
	}
	return expiry
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
//
// A non-positive expiry uses Config.PresignExpiry. The options are folded with
// storage.ApplyPresignGetOptions and mapped onto the S3 response-header
// overrides (response-content-disposition, response-content-type), which S3
// signs as part of the URL: the default "attachment" disposition and
// "application/octet-stream" type cannot be stripped by whoever holds the
// link.
func (s *S3Storage) PresignedGetURL(ctx context.Context, key string, expiry time.Duration, opts ...storage.PresignGetOption) (string, error) {
	o := storage.ApplyPresignGetOptions(opts...)

	input := &s3.GetObjectInput{
		Bucket:                     aws.String(s.bucket),
		Key:                        aws.String(key),
		ResponseContentDisposition: aws.String(o.ContentDisposition),
		ResponseContentType:        aws.String(o.ContentType),
	}

	req, err := s.presignClient.PresignGetObject(ctx, input, func(po *s3.PresignOptions) {
		po.Expires = s.effectiveExpiry(expiry)
	})
	if err != nil {
		return "", fmt.Errorf("storage: presign get %q: %w", key, err)
	}
	return req.URL, nil
}

// PresignedPutURL generates a temporary upload URL.
// A non-positive expiry uses Config.PresignExpiry.
func (s *S3Storage) PresignedPutURL(ctx context.Context, key string, contentType string, expiry time.Duration) (string, error) {
	req, err := s.presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, func(o *s3.PresignOptions) {
		o.Expires = s.effectiveExpiry(expiry)
	})
	if err != nil {
		return "", fmt.Errorf("storage: presign put %q: %w", key, err)
	}
	return req.URL, nil
}
