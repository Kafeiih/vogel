package config

import (
	"os"
	"testing"
	"time"
)

func TestParseStorageConfigs_NoStorages(t *testing.T) {
	os.Unsetenv("STORAGE_CONFIGS")

	cfgs := parseStorageConfigs()

	if len(cfgs) != 0 {
		t.Errorf("expected empty map, got %d entries", len(cfgs))
	}
}

func TestParseStorageConfigs_SingleStorage(t *testing.T) {
	t.Setenv("STORAGE_CONFIGS", "public")
	t.Setenv("STORAGE_PUBLIC_BUCKET", "my-assets")
	t.Setenv("STORAGE_PUBLIC_REGION", "sa-east-1")

	cfgs := parseStorageConfigs()

	if len(cfgs) != 1 {
		t.Fatalf("expected 1 storage, got %d", len(cfgs))
	}

	pub, ok := cfgs["public"]
	if !ok {
		t.Fatal("expected 'public' storage config")
	}
	if pub.Bucket != "my-assets" {
		t.Errorf("Bucket: got %q, want %q", pub.Bucket, "my-assets")
	}
	if pub.Region != "sa-east-1" {
		t.Errorf("Region: got %q, want %q", pub.Region, "sa-east-1")
	}
	if pub.PresignExpiry != 15*time.Minute {
		t.Errorf("PresignExpiry: got %v, want %v", pub.PresignExpiry, 15*time.Minute)
	}
}

func TestParseStorageConfigs_MultiStorage(t *testing.T) {
	t.Setenv("STORAGE_CONFIGS", "public,private")

	// Public → AWS S3
	t.Setenv("STORAGE_PUBLIC_BUCKET", "cdn-assets")
	t.Setenv("STORAGE_PUBLIC_REGION", "us-east-1")

	// Private → MinIO
	t.Setenv("STORAGE_PRIVATE_BUCKET", "documentos")
	t.Setenv("STORAGE_PRIVATE_REGION", "us-east-1")
	t.Setenv("STORAGE_PRIVATE_ENDPOINT", "http://minio:9000")
	t.Setenv("STORAGE_PRIVATE_ACCESS_KEY", "minioadmin")
	t.Setenv("STORAGE_PRIVATE_SECRET_KEY", "minioadmin")
	t.Setenv("STORAGE_PRIVATE_PATH_STYLE", "true")
	t.Setenv("STORAGE_PRIVATE_PRESIGN_EXPIRY", "30m")

	cfgs := parseStorageConfigs()

	if len(cfgs) != 2 {
		t.Fatalf("expected 2 storages, got %d", len(cfgs))
	}

	pub := cfgs["public"]
	if pub.Bucket != "cdn-assets" {
		t.Errorf("public.Bucket: got %q, want %q", pub.Bucket, "cdn-assets")
	}
	if pub.Endpoint != "" {
		t.Errorf("public.Endpoint: got %q, want empty", pub.Endpoint)
	}

	priv := cfgs["private"]
	if priv.Bucket != "documentos" {
		t.Errorf("private.Bucket: got %q, want %q", priv.Bucket, "documentos")
	}
	if priv.Endpoint != "http://minio:9000" {
		t.Errorf("private.Endpoint: got %q, want %q", priv.Endpoint, "http://minio:9000")
	}
	if priv.PathStyle != true {
		t.Error("private.PathStyle: got false, want true")
	}
	if priv.PresignExpiry != 30*time.Minute {
		t.Errorf("private.PresignExpiry: got %v, want %v", priv.PresignExpiry, 30*time.Minute)
	}
}

func TestParseStorageConfigs_WhitespaceHandling(t *testing.T) {
	t.Setenv("STORAGE_CONFIGS", " public , private ")
	t.Setenv("STORAGE_PUBLIC_BUCKET", "a")
	t.Setenv("STORAGE_PRIVATE_BUCKET", "b")

	cfgs := parseStorageConfigs()

	if len(cfgs) != 2 {
		t.Fatalf("expected 2 storages, got %d", len(cfgs))
	}
	if _, ok := cfgs["public"]; !ok {
		t.Error("expected 'public' key (trimmed)")
	}
	if _, ok := cfgs["private"]; !ok {
		t.Error("expected 'private' key (trimmed)")
	}
}

func TestParseStorageConfigs_EmptyNameSkipped(t *testing.T) {
	t.Setenv("STORAGE_CONFIGS", "public,,private,")
	t.Setenv("STORAGE_PUBLIC_BUCKET", "a")
	t.Setenv("STORAGE_PRIVATE_BUCKET", "b")

	cfgs := parseStorageConfigs()

	if len(cfgs) != 2 {
		t.Fatalf("expected 2 storages (empty names skipped), got %d", len(cfgs))
	}
}
