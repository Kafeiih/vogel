package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// PoolMetricsCollector exports pgxpool stats as Prometheus metrics.
//
// Unlike a package-level prometheus.MustRegister, NewPoolMetricsCollector is
// safe to call more than once against the same Registerer: a
// prometheus.AlreadyRegisteredError is handled by reusing the already
// registered collector instead of panicking. This matters for a library — a
// consumer may build two pools (e.g. in a test that constructs a fresh app per
// test case) and must not have the second one panic the process.
//
// Reusing the existing collector means a second NewPoolMetricsCollector call
// against the same Registerer returns the FIRST pool's collector, still bound
// to the first pool. If a consumer genuinely needs metrics for two distinct,
// simultaneously-live pools, register each with its own prometheus.Registerer
// (e.g. a dedicated prometheus.NewRegistry()) rather than sharing one.
type PoolMetricsCollector struct {
	pool *pgxpool.Pool

	totalConns           *prometheus.Desc
	idleConns            *prometheus.Desc
	acquiredConns        *prometheus.Desc
	constructingConns    *prometheus.Desc
	acquireCount         *prometheus.Desc
	emptyAcquireCount    *prometheus.Desc
	canceledAcquireCount *prometheus.Desc
	acquireDuration      *prometheus.Desc
	maxConns             *prometheus.Desc
}

// NewPoolMetricsCollector creates a collector for pool and registers it on reg.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewPoolMetricsCollector(pool *pgxpool.Pool, reg prometheus.Registerer) (*PoolMetricsCollector, error) {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	c := &PoolMetricsCollector{
		pool: pool,
		totalConns: prometheus.NewDesc(
			"db_pool_connections_total",
			"Total number of connections in the pool.",
			nil, nil,
		),
		idleConns: prometheus.NewDesc(
			"db_pool_connections_idle",
			"Number of idle connections in the pool.",
			nil, nil,
		),
		acquiredConns: prometheus.NewDesc(
			"db_pool_connections_acquired",
			"Number of currently acquired connections.",
			nil, nil,
		),
		constructingConns: prometheus.NewDesc(
			"db_pool_connections_constructing",
			"Number of connections being constructed.",
			nil, nil,
		),
		acquireCount: prometheus.NewDesc(
			"db_pool_acquire_total",
			"Total number of successful connection acquires.",
			nil, nil,
		),
		emptyAcquireCount: prometheus.NewDesc(
			"db_pool_acquire_empty_total",
			"Total acquires that had to create a new connection (pool was empty).",
			nil, nil,
		),
		canceledAcquireCount: prometheus.NewDesc(
			"db_pool_acquire_canceled_total",
			"Total acquires that were canceled.",
			nil, nil,
		),
		acquireDuration: prometheus.NewDesc(
			"db_pool_acquire_duration_seconds",
			"Total cumulative time spent acquiring connections.",
			nil, nil,
		),
		maxConns: prometheus.NewDesc(
			"db_pool_connections_max",
			"Maximum number of connections allowed in the pool.",
			nil, nil,
		),
	}

	registered, err := registerCollector(reg, c)
	if err != nil {
		return nil, err
	}
	if existing, ok := registered.(*PoolMetricsCollector); ok {
		return existing, nil
	}
	return c, nil
}

// registerCollector registers c on reg, returning the already-registered
// collector instead of erroring when c is a duplicate registration.
func registerCollector(reg prometheus.Registerer, c prometheus.Collector) (prometheus.Collector, error) {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			return are.ExistingCollector, nil
		}
		return nil, err
	}
	return c, nil
}

func (c *PoolMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.totalConns
	ch <- c.idleConns
	ch <- c.acquiredConns
	ch <- c.constructingConns
	ch <- c.acquireCount
	ch <- c.emptyAcquireCount
	ch <- c.canceledAcquireCount
	ch <- c.acquireDuration
	ch <- c.maxConns
}

func (c *PoolMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	stats := c.pool.Stat()

	ch <- prometheus.MustNewConstMetric(c.totalConns, prometheus.GaugeValue, float64(stats.TotalConns()))
	ch <- prometheus.MustNewConstMetric(c.idleConns, prometheus.GaugeValue, float64(stats.IdleConns()))
	ch <- prometheus.MustNewConstMetric(c.acquiredConns, prometheus.GaugeValue, float64(stats.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.constructingConns, prometheus.GaugeValue, float64(stats.ConstructingConns()))
	ch <- prometheus.MustNewConstMetric(c.acquireCount, prometheus.CounterValue, float64(stats.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.emptyAcquireCount, prometheus.CounterValue, float64(stats.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.canceledAcquireCount, prometheus.CounterValue, float64(stats.CanceledAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.acquireDuration, prometheus.CounterValue, stats.AcquireDuration().Seconds())
	ch <- prometheus.MustNewConstMetric(c.maxConns, prometheus.GaugeValue, float64(stats.MaxConns()))
}
