package s3

import (
	"log/slog"
	"testing"
	"time"
)

func TestNewS3Storage_EmptyBucket(t *testing.T) {
	cfg := Config{
		Bucket: "",
		Region: "us-east-1",
	}

	_, err := NewS3Storage(cfg, slog.Default())
	if err == nil {
		t.Fatal("expected error for empty bucket")
	}
}

func TestNewS3Storage_NilLogger(t *testing.T) {
	cfg := Config{
		Bucket: "test-bucket",
		Region: "us-east-1",
	}

	_, err := NewS3Storage(cfg, nil)
	if err == nil {
		t.Fatal("expected error for nil logger")
	}
}

func TestNewS3Storage_ValidConfig(t *testing.T) {
	cfg := Config{
		Bucket:        "test-bucket",
		Region:        "us-east-1",
		PresignExpiry: 15 * time.Minute,
	}

	s, err := NewS3Storage(cfg, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil storage")
	}
}

func TestNewS3Storage_MinIOConfig(t *testing.T) {
	cfg := Config{
		Bucket:        "docs",
		Region:        "us-east-1",
		Endpoint:      "http://localhost:9000",
		AccessKey:     "minioadmin",
		SecretKey:     "minioadmin",
		PathStyle:     true,
		PresignExpiry: 30 * time.Minute,
	}

	s, err := NewS3Storage(cfg, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error for MinIO config: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil storage for MinIO config")
	}
}
