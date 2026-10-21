package process

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"

	domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
)

type Catalog struct {
	fields   *domain.FieldRegistry
	engine   *workflow.Engine
	versions VersionRepository

	mu     sync.RWMutex
	byKey  map[string]*domain.Process
	latest map[string]int
}

func NewCatalog(fields *domain.FieldRegistry, engine *workflow.Engine, versions VersionRepository) *Catalog {
	return &Catalog{
		fields:   fields,
		engine:   engine,
		versions: versions,
		byKey:    map[string]*domain.Process{},
		latest:   map[string]int{},
	}
}

func (c *Catalog) Publish(ctx context.Context, db pgxtx.DBTX, spec domain.ProcessSpec) (*domain.Process, error) {
	// The version is assigned by the insert; nothing compiled depends on it.
	p, err := domain.CompileProcess(c.fields, spec, 1)
	if err != nil {
		return nil, fmt.Errorf("publish %q: %w", spec.Name, err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("publish %q: encode spec: %w", spec.Name, err)
	}
	version, err := c.versions.Insert(ctx, db, spec.Name, raw)
	if err != nil {
		return nil, fmt.Errorf("publish %q: store: %w", spec.Name, err)
	}
	p.Definition.Version = version
	if err := c.add(p); err != nil {
		return nil, fmt.Errorf("publish %q: %w", spec.Name, err)
	}
	return p, nil
}

func (c *Catalog) Load(ctx context.Context, db pgxtx.DBTX) error {
	stored, err := c.versions.All(ctx, db)
	if err != nil {
		return fmt.Errorf("load processes: %w", err)
	}
	for _, sv := range stored {
		spec, err := domain.ParseProcessSpec(sv.Spec)
		if err != nil {
			return fmt.Errorf("load processes: %w", err)
		}
		p, err := domain.CompileProcess(c.fields, spec, sv.Version)
		if err != nil {
			return fmt.Errorf("load %s@%d: %w", spec.Name, sv.Version, err)
		}
		if err := c.add(p); err != nil {
			return fmt.Errorf("load %s@%d: %w", spec.Name, sv.Version, err)
		}
	}
	return nil
}

func (c *Catalog) EnsurePublished(ctx context.Context, db pgxtx.DBTX, raw []byte) error {
	spec, err := domain.ParseProcessSpec(raw)
	if err != nil {
		return err
	}
	if _, ok := c.Latest(spec.Name); ok {
		return nil
	}
	_, err = c.Publish(ctx, db, spec)
	return err
}

func (c *Catalog) add(p *domain.Process) error {
	if err := c.engine.Register(p.Definition); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byKey[p.Definition.Key()] = p
	if p.Definition.Version > c.latest[p.Definition.Name] {
		c.latest[p.Definition.Name] = p.Definition.Version
	}
	return nil
}

func (c *Catalog) Get(name string, version int) (*domain.Process, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.byKey[workflow.Definition{Name: name, Version: version}.Key()]
	return p, ok
}

func (c *Catalog) Latest(name string) (*domain.Process, bool) {
	c.mu.RLock()
	v, ok := c.latest[name]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return c.Get(name, v)
}

func (c *Catalog) All() []*domain.Process {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*domain.Process, 0, len(c.byKey))
	for _, p := range c.byKey {
		out = append(out, p)
	}
	return out
}
