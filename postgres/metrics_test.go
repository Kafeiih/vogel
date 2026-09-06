package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePool builds a *pgxpool.Pool without connecting to a real database, by
// constructing it directly from a parsed config. pgxpool.NewWithConfig only
// dials lazily on first use, so this is safe for a collector-construction test
// that never calls Collect against a live pool.
func fakePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://u:p@localhost:5432/db?sslmode=disable")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// TestNewPoolMetricsCollector_DoubleConstruction_DoesNotPanic is the regression
// test for FIX 4: the source's PoolMetricsCollector called
// prometheus.MustRegister unconditionally, which panics on the second call
// against the same registry (e.g. two pools built in two test cases sharing
// prometheus.DefaultRegisterer).
func TestNewPoolMetricsCollector_DoubleConstruction_DoesNotPanic(t *testing.T) {
	reg := prometheus.NewRegistry()
	pool1 := fakePool(t)
	pool2 := fakePool(t)

	assert.NotPanics(t, func() {
		c1, err := NewPoolMetricsCollector(pool1, reg)
		require.NoError(t, err)
		require.NotNil(t, c1)

		c2, err := NewPoolMetricsCollector(pool2, reg)
		require.NoError(t, err)
		require.NotNil(t, c2)
	})
}

// TestNewPoolMetricsCollector_NilRegistererUsesDefault verifies the nil ->
// DefaultRegisterer fallback, mirroring httpx/middleware.NewMetrics.
func TestNewPoolMetricsCollector_NilRegistererUsesDefault(t *testing.T) {
	// Use a fresh pool with a name unlikely to collide; DefaultRegisterer is
	// process-global, so only assert this call itself succeeds without panicking.
	pool := fakePool(t)

	// Unregister any leftover collector at the very end so other tests in this
	// package don't collide against prometheus.DefaultRegisterer's global state.
	c, err := NewPoolMetricsCollector(pool, nil)
	require.NoError(t, err)
	require.NotNil(t, c)
	t.Cleanup(func() { prometheus.DefaultRegisterer.Unregister(c) })
}

// TestPoolMetricsCollector_DescribeAndCollect verifies the collector emits all
// nine expected descriptors and metrics without touching a live database
// (pgxpool.Stat() works against an unconnected pool).
func TestPoolMetricsCollector_DescribeAndCollect(t *testing.T) {
	reg := prometheus.NewRegistry()
	pool := fakePool(t)

	c, err := NewPoolMetricsCollector(pool, reg)
	require.NoError(t, err)

	descCh := make(chan *prometheus.Desc, 16)
	c.Describe(descCh)
	close(descCh)
	var descCount int
	for range descCh {
		descCount++
	}
	assert.Equal(t, 9, descCount)

	metricCh := make(chan prometheus.Metric, 16)
	c.Collect(metricCh)
	close(metricCh)
	var metricCount int
	for range metricCh {
		metricCount++
	}
	assert.Equal(t, 9, metricCount)
}
