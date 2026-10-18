// Package s3 provides the S3-compatible implementation of the storage.Storage port.
//
// Works with AWS S3, MinIO, DigitalOcean Spaces, Backblaze B2, and any S3-compatible service.
// For MinIO, set Endpoint and PathStyle=true in Config.
package s3

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

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

	// MaxPresignExpiry is the longest expiry a presign call may ask for; a
	// longer one returns storage.ErrPresignExpiryTooLong. A presigned URL is
	// a bearer credential nothing can revoke before it expires, so its life
	// is bounded. Zero means DefaultMaxPresignExpiry. A negative value, one
	// above MaxSigV4PresignExpiry, or a maximum below the effective
	// PresignExpiry (DefaultPresignExpiry, 15m, when unset) is a construction
	// error.
	MaxPresignExpiry time.Duration

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

// DefaultMaxPresignExpiry is the longest presigned URL lifetime allowed when
// Config.MaxPresignExpiry is zero.
const DefaultMaxPresignExpiry = time.Hour

// MaxSigV4PresignExpiry is the longest lifetime a SigV4 presigned URL can
// have; S3 rejects a longer X-Amz-Expires when the URL is used.
const MaxSigV4PresignExpiry = 7 * 24 * time.Hour

// Storage implements storage.Storage using AWS SDK v2.
type S3Storage struct {
	client        *s3.Client
	presignClient *s3.PresignClient
	bucket        string
	presignExpiry time.Duration
	maxExpiry     time.Duration
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
	if cfg.MaxPresignExpiry < 0 {
		return nil, fmt.Errorf("storage: max presign expiry must not be negative")
	}
	if cfg.MaxPresignExpiry > MaxSigV4PresignExpiry {
		return nil, fmt.Errorf("storage: max presign expiry %s exceeds the %s SigV4 allows", cfg.MaxPresignExpiry, MaxSigV4PresignExpiry)
	}
	maxExpiry := cfg.MaxPresignExpiry
	if maxExpiry == 0 {
		maxExpiry = DefaultMaxPresignExpiry
	}
	presignExpiry := cfg.PresignExpiry
	if presignExpiry <= 0 {
		presignExpiry = DefaultPresignExpiry
	}
	if presignExpiry > maxExpiry {
		return nil, fmt.Errorf("storage: presign expiry %s exceeds the maximum %s", presignExpiry, maxExpiry)
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

	return &S3Storage{
		client:        client,
		presignClient: presignClient,
		bucket:        cfg.Bucket,
		presignExpiry: presignExpiry,
		maxExpiry:     maxExpiry,
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
// configured with". An expiry above the maximum returns
// storage.ErrPresignExpiryTooLong.
func (s *S3Storage) effectiveExpiry(expiry time.Duration) (time.Duration, error) {
	if expiry <= 0 {
		return s.presignExpiry, nil
	}
	if expiry > s.maxExpiry {
		return 0, fmt.Errorf("%w: %s > %s", storage.ErrPresignExpiryTooLong, expiry, s.maxExpiry)
	}
	return expiry, nil
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
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, fmt.Errorf("storage: download %q: %w", key, storage.ErrNotFound)
		}
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
	expires, err := s.effectiveExpiry(expiry)
	if err != nil {
		return "", fmt.Errorf("storage: presign get %q: %w", key, err)
	}

	input := &s3.GetObjectInput{
		Bucket:                     aws.String(s.bucket),
		Key:                        aws.String(key),
		ResponseContentDisposition: aws.String(o.ContentDisposition),
		ResponseContentType:        aws.String(o.ContentType),
	}

	req, err := s.presignClient.PresignGetObject(ctx, input, func(po *s3.PresignOptions) {
		po.Expires = expires
	})
	if err != nil {
		return "", fmt.Errorf("storage: presign get %q: %w", key, err)
	}
	return req.URL, nil
}

// PresignedPutURL generates a temporary upload URL.
// A non-positive expiry uses Config.PresignExpiry. With
// storage.WithContentLength the URL also signs Content-Length, so S3 rejects
// a body of any other size.
func (s *S3Storage) PresignedPutURL(ctx context.Context, key string, contentType string, expiry time.Duration, opts ...storage.PresignPutOption) (string, error) {
	o := storage.ApplyPresignPutOptions(opts...)
	expires, err := s.effectiveExpiry(expiry)
	if err != nil {
		return "", fmt.Errorf("storage: presign put %q: %w", key, err)
	}
	if o.HasContentLength && o.ContentLength <= 0 {
		return "", fmt.Errorf("storage: presign put %q: %w", key, storage.ErrInvalidContentLength)
	}

	input := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}
	if o.HasContentLength {
		input.ContentLength = aws.Int64(o.ContentLength)
	}

	req, err := s.presignClient.PresignPutObject(ctx, input, func(po *s3.PresignOptions) {
		po.Expires = expires
	})
	if err != nil {
		return "", fmt.Errorf("storage: presign put %q: %w", key, err)
	}
	return req.URL, nil
}
