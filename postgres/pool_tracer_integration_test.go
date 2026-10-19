//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/postgres"
)

// syncBuffer is a bytes.Buffer safe to read while the pool logs from other
// goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestPoolTracer_Integration exercises AcquireTimeout, LeakThreshold and the
// pool histograms against a real PostgreSQL, sharing one container.
func TestPoolTracer_Integration(t *testing.T) {
	connStr := startTestPostgres(t)
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("exhausted pool fails within AcquireTimeout", func(t *testing.T) {
		ctx := context.Background()
		pool, err := postgres.NewPool(ctx, postgres.Config{
			URL: connStr, MaxConns: 1, AcquireTimeout: 200 * time.Millisecond,
		}, discard)
		require.NoError(t, err)
		defer pool.Close()

		held, err := pool.Acquire(ctx)
		require.NoError(t, err)
		defer held.Release()

		// A generous caller deadline, only so a regression fails instead of
		// hanging the suite; AcquireTimeout must cut the wait far earlier.
		callerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		start := time.Now()
		_, err = pool.Acquire(callerCtx)
		elapsed := time.Since(start)

		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.NoError(t, callerCtx.Err(), "the caller's own context has not expired: AcquireTimeout cut the wait")
		assert.GreaterOrEqual(t, elapsed, 150*time.Millisecond)
		assert.Less(t, elapsed, 2*time.Second, "the wait must be bounded by AcquireTimeout")
	})

	t.Run("zero AcquireTimeout waits for the caller's context", func(t *testing.T) {
		pool, err := postgres.NewPool(context.Background(), postgres.Config{URL: connStr, MaxConns: 1}, discard)
		require.NoError(t, err)
		defer pool.Close()

		held, err := pool.Acquire(context.Background())
		require.NoError(t, err)
		defer held.Release()

		ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err = pool.Acquire(ctx)
		elapsed := time.Since(start)

		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.GreaterOrEqual(t, elapsed, 650*time.Millisecond,
			"without AcquireTimeout the wait lasts until the caller's deadline")
	})

	t.Run("connection held past LeakThreshold logs the acquiring caller", func(t *testing.T) {
		var logs syncBuffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		ctx := context.Background()
		pool, err := postgres.NewPool(ctx, postgres.Config{
			URL: connStr, MaxConns: 2, LeakThreshold: 100 * time.Millisecond,
		}, logger)
		require.NoError(t, err)
		defer pool.Close()

		conn, err := pool.Acquire(ctx)
		require.NoError(t, err)
		_, err = conn.Exec(ctx, "SELECT 1")
		require.NoError(t, err)
		time.Sleep(250 * time.Millisecond)
		conn.Release()

		out := logs.String()
		require.Contains(t, out, "level=WARN")
		assert.Contains(t, out, `msg="database connection held longer than the leak threshold"`)
		assert.Contains(t, out, "pool_tracer_integration_test.go:")
		assert.Contains(t, out, "postgres_test.TestPoolTracer_Integration")
		assert.NotContains(t, out, "SELECT 1", "the leak WARN must never carry SQL")
	})

	t.Run("histograms observe acquire wait and hold", func(t *testing.T) {
		ctx := context.Background()
		pool, err := postgres.NewPool(ctx, postgres.Config{URL: connStr, MaxConns: 2}, discard)
		require.NoError(t, err)
		defer pool.Close()

		reg := prometheus.NewRegistry()
		_, err = postgres.NewPoolMetricsCollector(pool, reg)
		require.NoError(t, err)

		for range 3 {
			_, err := pool.Exec(ctx, "SELECT 1")
			require.NoError(t, err)
		}

		families, err := reg.Gather()
		require.NoError(t, err)
		counts := map[string]uint64{}
		for _, f := range families {
			if strings.HasSuffix(f.GetName(), "_seconds") && len(f.GetMetric()) == 1 && f.GetMetric()[0].GetHistogram() != nil {
				counts[f.GetName()] = f.GetMetric()[0].GetHistogram().GetSampleCount()
			}
		}
		// NewPool's own Ping acquires once too, hence at least 4.
		assert.GreaterOrEqual(t, counts["db_pool_acquire_wait_seconds"], uint64(4), "counts: %v", counts)
		assert.GreaterOrEqual(t, counts["db_pool_conn_hold_seconds"], uint64(4), "counts: %v", counts)
	})
}
