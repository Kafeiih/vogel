package s3

import (
	"fmt"
	"log/slog"
	"sort"

	domainStorage "github.com/kafeiih/vogel/storage"
)

// StorageRegistry holds named Storage instances.
//
// Usage:
//
//	reg, err := NewStorageRegistry(cfgs, logger)
//	publicStore := reg.Get("public")   // → AWS S3
//	privateStore := reg.Get("private") // → MinIO
type StorageRegistry struct {
	storages map[string]domainStorage.Storage
}

// NewStorageRegistry creates a Storage instance for each named config.
// Returns an error if any individual storage fails to initialize.
func NewStorageRegistry(cfgs map[string]Config, logger *slog.Logger) (*StorageRegistry, error) {
	storages := make(map[string]domainStorage.Storage, len(cfgs))

	for name, cfg := range cfgs {
		s, err := NewS3Storage(cfg, logger)
		if err != nil {
			return nil, fmt.Errorf("storage %q: %w", name, err)
		}
		storages[name] = s
	}

	return &StorageRegistry{storages: storages}, nil
}

// Get returns the Storage for the given name, or nil if not found.
func (r *StorageRegistry) Get(name string) domainStorage.Storage {
	return r.storages[name]
}

// Len returns the number of registered storages.
func (r *StorageRegistry) Len() int {
	return len(r.storages)
}

// Names returns the sorted list of registered storage names.
func (r *StorageRegistry) Names() []string {
	names := make([]string, 0, len(r.storages))
	for name := range r.storages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
