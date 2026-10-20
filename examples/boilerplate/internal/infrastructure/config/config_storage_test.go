package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kafeiih/vogel/config"
	"github.com/kafeiih/vogel/storage/s3"
)

func TestParseStorageConfigs_NoStorages(t *testing.T) {
	os.Unsetenv("STORAGE_CONFIGS")

	cfgs := parseStorageConfigsNoErr(t)

	if len(cfgs) != 0 {
		t.Errorf("expected empty map, got %d entries", len(cfgs))
	}
}

func TestParseStorageConfigs_SingleStorage(t *testing.T) {
	t.Setenv("STORAGE_CONFIGS", "public")
	t.Setenv("STORAGE_PUBLIC_BUCKET", "my-assets")
	t.Setenv("STORAGE_PUBLIC_REGION", "sa-east-1")

	cfgs := parseStorageConfigsNoErr(t)

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

	t.Setenv("STORAGE_PUBLIC_BUCKET", "cdn-assets")
	t.Setenv("STORAGE_PUBLIC_REGION", "us-east-1")

	t.Setenv("STORAGE_PRIVATE_BUCKET", "documentos")
	t.Setenv("STORAGE_PRIVATE_REGION", "us-east-1")
	t.Setenv("STORAGE_PRIVATE_ENDPOINT", "http://minio:9000")
	t.Setenv("STORAGE_PRIVATE_ACCESS_KEY", "minioadmin")
	t.Setenv("STORAGE_PRIVATE_SECRET_KEY", "minioadmin")
	t.Setenv("STORAGE_PRIVATE_PATH_STYLE", "true")
	t.Setenv("STORAGE_PRIVATE_PRESIGN_EXPIRY", "30m")

	cfgs := parseStorageConfigsNoErr(t)

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

	cfgs := parseStorageConfigsNoErr(t)

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

	cfgs := parseStorageConfigsNoErr(t)

	if len(cfgs) != 2 {
		t.Fatalf("expected 2 storages (empty names skipped), got %d", len(cfgs))
	}
}

func parseStorageConfigsNoErr(t *testing.T) map[string]s3.Config {
	t.Helper()
	var errs config.Errors
	cfgs := parseStorageConfigs(&errs)
	if err := errs.Err(); err != nil {
		t.Fatalf("parseStorageConfigs: unexpected error: %v", err)
	}
	return cfgs
}

func TestParseStorageConfigs_InvalidPresignExpiry_IsConfigError(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"15", "STORAGE_DOCS_PRESIGN_EXPIRY"},
		{"soon", "STORAGE_DOCS_PRESIGN_EXPIRY"},
		{"0s", "STORAGE_DOCS_PRESIGN_EXPIRY: must be greater than zero"},
		{"-5m", "STORAGE_DOCS_PRESIGN_EXPIRY: must be greater than zero"},
		{"2h", "STORAGE_DOCS_PRESIGN_EXPIRY: must be at most 1h0m0s, got 2h0m0s"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("STORAGE_CONFIGS", "docs")
			t.Setenv("STORAGE_DOCS_BUCKET", "b")
			t.Setenv("STORAGE_DOCS_PRESIGN_EXPIRY", tc.value)

			var errs config.Errors
			parseStorageConfigs(&errs)

			err := errs.Err()
			if err == nil {
				t.Fatalf("errs.Err() = nil, want an error for PRESIGN_EXPIRY=%q", tc.value)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestParseStorageConfigs_PresignExpiryAtMaximum_IsAccepted(t *testing.T) {
	t.Setenv("STORAGE_CONFIGS", "docs")
	t.Setenv("STORAGE_DOCS_BUCKET", "b")
	t.Setenv("STORAGE_DOCS_PRESIGN_EXPIRY", "1h")

	if got := parseStorageConfigsNoErr(t)["docs"].PresignExpiry; got != time.Hour {
		t.Errorf("PresignExpiry = %v, want 1h", got)
	}
}
