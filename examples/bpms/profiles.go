package main

import (
	"context"
	"slices"
	"sync"

	"github.com/kafeiih/vogel/workflow"
)

// ProfileResolver says who holds a profile right now. A definition only
// names profiles; this port turns one into people at the moment of asking,
// so a change of holder (a new encargado, a subrogation starting or ending)
// takes effect without touching a definition or reassigning a case.
//
// In production this is a client of the organization's directory (SAFO);
// FakeSAFO stands in for it here.
type ProfileResolver interface {
	Holders(ctx context.Context, position, unit string) ([]string, error)
}

// holds reports whether actor holds the eligibility right now.
func holds(ctx context.Context, r ProfileResolver, e workflow.Eligibility, actor string) (bool, error) {
	if e.IsZero() {
		return false, nil
	}
	people, err := r.Holders(ctx, e.Position, e.Unit)
	if err != nil {
		return false, err
	}
	return slices.Contains(people, actor), nil
}

type profileKey struct{ position, unit string }

// FakeSAFO is an in-memory directory with SAFO's rule for a unit's
// encargado slot: one titular, and at most one subrogante in force, who
// wins over the titular while the subrogation lasts.
type FakeSAFO struct {
	mu          sync.RWMutex
	titulars    map[profileKey]string
	subrogantes map[profileKey]string
}

// NewFakeSAFO returns an empty directory.
func NewFakeSAFO() *FakeSAFO {
	return &FakeSAFO{titulars: map[profileKey]string{}, subrogantes: map[profileKey]string{}}
}

// SetTitular puts actor in the profile.
func (f *FakeSAFO) SetTitular(position, unit, actor string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.titulars[profileKey{position, unit}] = actor
}

// SetSubrogante starts a subrogation, replacing any one in force.
func (f *FakeSAFO) SetSubrogante(position, unit, actor string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subrogantes[profileKey{position, unit}] = actor
}

// EndSubrogation ends the subrogation in force, if any. The titular needs
// nothing to return: it never stopped holding the profile.
func (f *FakeSAFO) EndSubrogation(position, unit string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subrogantes, profileKey{position, unit})
}

// Holders implements ProfileResolver.
func (f *FakeSAFO) Holders(_ context.Context, position, unit string) ([]string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	k := profileKey{position, unit}
	if s, ok := f.subrogantes[k]; ok {
		return []string{s}, nil
	}
	if t, ok := f.titulars[k]; ok {
		return []string{t}, nil
	}
	return nil, nil
}
