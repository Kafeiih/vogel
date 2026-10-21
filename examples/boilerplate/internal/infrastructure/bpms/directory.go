package bpms

import (
	"context"
	"slices"
	"sync"
)

type ProfileHolder struct {
	Position string
	Unit     string
	UserID   string
}

type profileKey struct{ position, unit string }

// Stands in for the organization's directory (SAFO); replace it with a client of it.
type MemoryDirectory struct {
	mu      sync.RWMutex
	holders map[profileKey][]string
}

func NewMemoryDirectory(holders ...ProfileHolder) *MemoryDirectory {
	d := &MemoryDirectory{holders: map[profileKey][]string{}}
	for _, h := range holders {
		k := profileKey{h.Position, h.Unit}
		d.holders[k] = append(d.holders[k], h.UserID)
	}
	return d
}

func (d *MemoryDirectory) Set(position, unit string, userIDs ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.holders[profileKey{position, unit}] = slices.Clone(userIDs)
}

func (d *MemoryDirectory) Holders(_ context.Context, position, unit string) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return slices.Clone(d.holders[profileKey{position, unit}]), nil
}
