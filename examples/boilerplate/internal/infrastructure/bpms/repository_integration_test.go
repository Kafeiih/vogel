//go:build integration

package bpms_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
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

func TestVersionRepository_ConcurrentInsertOfOneNameIsAConflict(t *testing.T) {
	ctx := context.Background()
	pool := bpmstest.NewPool(t)
	repo := bpms.NewVersionRepository()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	v, err := repo.Insert(ctx, tx, "consulta", []byte(`{"name":"consulta"}`))
	require.NoError(t, err)
	require.Equal(t, 1, v)

	done := make(chan error, 1)
	go func() {
		_, err := repo.Insert(ctx, pool, "consulta", []byte(`{"name":"consulta"}`))
		done <- err
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE NOT granted)`).Scan(&waiting)
		return err == nil && waiting
	}, 10*time.Second, 20*time.Millisecond, "second insert never blocked on the first")
	require.NoError(t, tx.Commit(ctx))

	assert.ErrorIs(t, <-done, domain.ErrPublishConflict)
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

func TestNewDomain_ConcurrentBootsSeedOnce(t *testing.T) {
	ctx := context.Background()
	pool := bpmstest.NewPool(t)

	const replicas = 4
	errs := make(chan error, replicas)
	for range replicas {
		go func() {
			_, err := bpms.NewDomain(ctx, pool, bpmstest.NewEngine(), bpms.NewMemoryDirectory())
			errs <- err
		}()
	}
	for range replicas {
		require.NoError(t, <-errs)
	}

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM process_version WHERE name = 'consulta'`).Scan(&n))
	assert.Equal(t, 1, n)
}
