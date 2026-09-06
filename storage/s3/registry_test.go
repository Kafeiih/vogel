package s3

import (
	"log/slog"
	"testing"
	"time"
)

func TestNewStorageRegistry_Empty(t *testing.T) {
	cfgs := map[string]Config{}

	reg, err := NewStorageRegistry(cfgs, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}
	if reg.Len() != 0 {
		t.Errorf("Len: got %d, want 0", reg.Len())
	}
}

func TestNewStorageRegistry_SingleStorage(t *testing.T) {
	cfgs := map[string]Config{
		"public": {
			Bucket:        "assets",
			Region:        "us-east-1",
			PresignExpiry: 15 * time.Minute,
		},
	}

	reg, err := NewStorageRegistry(cfgs, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reg.Len() != 1 {
		t.Errorf("Len: got %d, want 1", reg.Len())
	}

	s := reg.Get("public")
	if s == nil {
		t.Fatal("expected 'public' storage")
	}
}

func TestNewStorageRegistry_MultiStorage(t *testing.T) {
	cfgs := map[string]Config{
		"public": {
			Bucket:        "cdn",
			Region:        "us-east-1",
			PresignExpiry: 15 * time.Minute,
		},
		"private": {
			Bucket:        "docs",
			Region:        "us-east-1",
			Endpoint:      "http://minio:9000",
			PathStyle:     true,
			PresignExpiry: 30 * time.Minute,
		},
	}

	reg, err := NewStorageRegistry(cfgs, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reg.Len() != 2 {
		t.Errorf("Len: got %d, want 2", reg.Len())
	}
	if reg.Get("public") == nil {
		t.Error("expected 'public' storage")
	}
	if reg.Get("private") == nil {
		t.Error("expected 'private' storage")
	}
}

func TestNewStorageRegistry_MissingBucketFails(t *testing.T) {
	cfgs := map[string]Config{
		"broken": {
			Bucket: "", // empty — should fail
			Region: "us-east-1",
		},
	}

	_, err := NewStorageRegistry(cfgs, slog.Default())
	if err == nil {
		t.Fatal("expected error for storage with empty bucket")
	}
}

func TestStorageRegistry_GetMissing(t *testing.T) {
	reg, _ := NewStorageRegistry(map[string]Config{}, slog.Default())

	s := reg.Get("nonexistent")
	if s != nil {
		t.Error("expected nil for missing storage name")
	}
}

func TestStorageRegistry_Names(t *testing.T) {
	cfgs := map[string]Config{
		"alpha": {Bucket: "a", Region: "us-east-1", PresignExpiry: time.Minute},
		"beta":  {Bucket: "b", Region: "us-east-1", PresignExpiry: time.Minute},
	}

	reg, err := NewStorageRegistry(cfgs, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := reg.Names()
	if len(names) != 2 {
		t.Fatalf("Names: got %d, want 2", len(names))
	}

	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["alpha"] || !found["beta"] {
		t.Errorf("Names: got %v, want [alpha, beta]", names)
	}
}
