package bpms

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/kafeiih/vogel/workflow"
	wfpostgres "github.com/kafeiih/vogel/workflow/postgres"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/application/process"
	domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
)

//go:embed processes/*.json
var seeds embed.FS

type Domain struct {
	Catalog *process.Catalog
	Service *process.Service
}

// Publishes each processes/*.json whose name has no stored version yet; replicas may boot at once.
func NewDomain(ctx context.Context, db process.DB, engine *workflow.Engine, profiles domain.ProfileResolver) (*Domain, error) {
	fields, err := domain.NewFieldRegistry(domain.DefaultFieldTypes()...)
	if err != nil {
		return nil, fmt.Errorf("bpms: %w", err)
	}
	catalog := process.NewCatalog(fields, engine, NewVersionRepository())
	if err := catalog.Load(ctx, db); err != nil {
		return nil, fmt.Errorf("bpms: %w", err)
	}

	files, err := fs.Glob(seeds, "processes/*.json")
	if err != nil {
		return nil, fmt.Errorf("bpms: %w", err)
	}
	for _, f := range files {
		raw, err := seeds.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("bpms: %w", err)
		}
		if err := catalog.EnsurePublished(ctx, db, raw); err != nil {
			return nil, fmt.Errorf("bpms: seeding %s: %w", f, err)
		}
	}

	svc := process.NewService(process.ServiceDeps{
		DB:       db,
		Engine:   engine,
		Cases:    wfpostgres.NewRepository(),
		Values:   NewValueRepository(),
		Catalog:  catalog,
		Profiles: profiles,
	})
	return &Domain{Catalog: catalog, Service: svc}, nil
}
