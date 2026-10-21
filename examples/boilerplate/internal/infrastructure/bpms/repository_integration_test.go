//go:build integration

package bpms_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/bpms"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/bpms/bpmstest"
)

func TestVersionRepository_AssignsIncreasingVersionsPerName(t *testing.T) {
	ctx := context.Background()
	pool := bpmstest.NewPool(t)
	repo := bpms.NewVersionRepository()

	for _, tc := range []struct {
		name string
		want int
	}{{"consulta", 1}, {"consulta", 2}, {"reclamo", 1}} {
		v, err := repo.Insert(ctx, pool, tc.name, []byte(`{"name":"`+tc.name+`"}`))
		require.NoError(t, err)
		assert.Equal(t, tc.want, v, tc.name)
	}

	all, err := repo.All(ctx, pool)
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, 1, all[0].Version)
	assert.JSONEq(t, `{"name":"consulta"}`, string(all[0].Spec))
}

func TestValueRepository_KeepsHistory(t *testing.T) {
	ctx := context.Background()
	pool := bpmstest.NewPool(t)
	repo := bpms.NewValueRepository()
	caseID := uuid.New()

	require.NoError(t, repo.Save(ctx, pool, caseID, "ingreso", "ana", map[string]any{"tema": "general", "nombre": "Ana"}))
	require.NoError(t, repo.Save(ctx, pool, caseID, "respuesta", "luis", map[string]any{"tema": "reclamo"}))

	current, err := repo.Current(ctx, pool, caseID)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"tema": "reclamo", "nombre": "Ana"}, current)

	history, err := repo.History(ctx, pool, caseID)
	require.NoError(t, err)
	require.Len(t, history, 3)
	assert.Equal(t, "nombre", history[0].Field)
	last := history[2]
	assert.Equal(t, "tema", last.Field)
	assert.Equal(t, "reclamo", last.Value)
	assert.Equal(t, "respuesta", last.NodeID)
	assert.Equal(t, "luis", last.ActorID)
	assert.False(t, last.RecordedAt.IsZero())
}

func TestNewDomain_SeedsConsultaOnce(t *testing.T) {
	ctx := context.Background()
	pool := bpmstest.NewPool(t)
	dir := bpms.NewMemoryDirectory()

	first, err := bpms.NewDomain(ctx, pool, bpmstest.NewEngine(), dir)
	require.NoError(t, err)
	p, ok := first.Catalog.Latest("consulta")
	require.True(t, ok)
	assert.Equal(t, 1, p.Definition.Version)

	engine := bpmstest.NewEngine()
	rebooted, err := bpms.NewDomain(ctx, pool, engine, dir)
	require.NoError(t, err)
	p, ok = rebooted.Catalog.Latest("consulta")
	require.True(t, ok)
	assert.Equal(t, 1, p.Definition.Version, "a restart reloads version 1 without publishing again")
	assert.Len(t, engine.Definitions(), 1)
}
