package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"
)

// Catalog publishes process specs and keeps every published version
// compiled and registered on the engine. A published version is immutable:
// publishing again always creates the next version, and a case keeps
// running on the version it was opened with.
type Catalog struct {
	reg    *FieldRegistry
	engine *workflow.Engine

	mu     sync.RWMutex
	byKey  map[string]*Process // "name@version"
	latest map[string]int
}

// NewCatalog returns an empty catalog. Call Load at boot.
func NewCatalog(reg *FieldRegistry, engine *workflow.Engine) *Catalog {
	return &Catalog{reg: reg, engine: engine, byKey: map[string]*Process{}, latest: map[string]int{}}
}

// Publish compiles spec, stores it as the next version of its name and
// registers it. Nothing is stored when the spec does not compile. Two
// concurrent publishes of the same name collide on the version's primary
// key and one of them fails; that is acceptable for an authoring action.
func (c *Catalog) Publish(ctx context.Context, db pgxtx.DBTX, spec ProcessSpec) (*Process, error) {
	// Version 1 is a placeholder for validation: nothing compiled depends
	// on the version number, so the real one is set after the insert.
	p, err := CompileProcess(c.reg, spec, 1)
	if err != nil {
		return nil, fmt.Errorf("publish %q: %w", spec.Name, err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("publish %q: encode spec: %w", spec.Name, err)
	}

	var version int
	err = db.QueryRow(ctx, `
		INSERT INTO process_version (name, version, spec)
		SELECT $1, COALESCE(MAX(version), 0) + 1, $2
		FROM process_version WHERE name = $1
		RETURNING version`, spec.Name, raw).Scan(&version)
	if err != nil {
		return nil, fmt.Errorf("publish %q: store: %w", spec.Name, err)
	}
	p.Definition.Version = version

	if err := c.add(p); err != nil {
		return nil, fmt.Errorf("publish %q: %w", spec.Name, err)
	}
	return p, nil
}

// Load compiles and registers every stored version, so cases opened on
// any version keep working after a restart. A stored version that no
// longer compiles (for example, a field type was removed from the
// registry) fails the boot instead of stranding its cases.
func (c *Catalog) Load(ctx context.Context, db pgxtx.DBTX) error {
	rows, err := db.Query(ctx, `SELECT version, spec FROM process_version ORDER BY name, version`)
	if err != nil {
		return fmt.Errorf("load processes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			version int
			raw     []byte
		)
		if err := rows.Scan(&version, &raw); err != nil {
			return fmt.Errorf("load processes: %w", err)
		}
		spec, err := ParseProcessSpec(raw)
		if err != nil {
			return fmt.Errorf("load processes: %w", err)
		}
		p, err := CompileProcess(c.reg, spec, version)
		if err != nil {
			return fmt.Errorf("load %s@%d: %w", spec.Name, version, err)
		}
		if err := c.add(p); err != nil {
			return fmt.Errorf("load %s@%d: %w", spec.Name, version, err)
		}
	}
	return rows.Err()
}

func (c *Catalog) add(p *Process) error {
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

// Get returns one published version.
func (c *Catalog) Get(name string, version int) (*Process, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.byKey[workflow.Definition{Name: name, Version: version}.Key()]
	return p, ok
}

// Latest returns the newest published version of name.
func (c *Catalog) Latest(name string) (*Process, bool) {
	c.mu.RLock()
	v, ok := c.latest[name]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return c.Get(name, v)
}

// All returns every published version.
func (c *Catalog) All() []*Process {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Process, 0, len(c.byKey))
	for _, p := range c.byKey {
		out = append(out, p)
	}
	return out
}
