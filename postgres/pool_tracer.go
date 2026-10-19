package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"
	"weak"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Names and help of the histograms the pool tracer records. They are shared
// with PoolMetricsCollector, which describes them for every pool so that two
// collectors on one registry always have the same descriptor set.
const (
	acquireWaitMetricName = "db_pool_acquire_wait_seconds"
	acquireWaitMetricHelp = "Time spent waiting in Pool.Acquire for a connection, successful or not " +
		"(a wait cut by AcquireTimeout is observed at roughly that bound)."
	connHoldMetricName = "db_pool_conn_hold_seconds"
	connHoldMetricHelp = "Time a connection stayed acquired, from a successful Acquire to its Release."
)

// poolHistogramBuckets span 1ms to 30s: a healthy acquire is sub-millisecond
// to a few milliseconds, a saturated pool shows up between 100ms and the
// AcquireTimeout (3s in the boilerplate), and holds past the leak threshold
// (10s there) land in the top buckets.
var poolHistogramBuckets = []float64{
	0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30,
}

// defaultInternalPrefixes are the function-name prefixes skipped when looking
// for the code that acquired a connection: pgx and its pool, and this module's
// own postgres and pgxtx packages, which acquire on the caller's behalf. The
// trailing "." or "/" keeps e.g. postgres_test frames from matching.
var defaultInternalPrefixes = []string{
	"github.com/jackc/pgx/",
	"github.com/jackc/puddle/",
	"github.com/kafeiih/vogel/postgres.",
	"github.com/kafeiih/vogel/pgxtx.",
}

// maxCallerFrames bounds the stack captured per acquire when LeakThreshold is
// set. pgxtx.WithTx -> pgxpool.Pool.BeginTx -> Acquire -> tracer is well
// under this, so the first application frame is always inside it.
const maxCallerFrames = 32

// poolTracerOptions configures newPoolTracer.
type poolTracerOptions struct {
	logger             *slog.Logger
	slowQueryThreshold time.Duration
	hashSlowQuerySQL   bool
	acquireTimeout     time.Duration
	leakThreshold      time.Duration
	// maxConns is the pool's MaxConns: more tracked holds than that means
	// some were hijacked, which triggers pruning (see pruneLocked).
	maxConns int
	// extra is Config.Tracer.
	extra pgx.QueryTracer
	// internalPrefixes overrides defaultInternalPrefixes (tests only).
	internalPrefixes []string
}

// poolTracer is the single pgx tracer NewPool installs. It composes
// SlowQueryTracer (pgx.QueryTracer, unchanged) with pgxpool's AcquireTracer
// and ReleaseTracer to:
//
//   - bound every Acquire with acquireTimeout (Config.AcquireTimeout);
//   - record db_pool_acquire_wait_seconds and db_pool_conn_hold_seconds;
//   - log a WARN naming the acquiring caller when a connection is released
//     after being held longer than leakThreshold (Config.LeakThreshold).
type poolTracer struct {
	*SlowQueryTracer

	// extra is Config.Tracer, called around this tracer's own work on every
	// hook it implements (see extra_tracer.go); nil when unset.
	extra pgx.QueryTracer

	logger           *slog.Logger
	acquireTimeout   time.Duration
	leakThreshold    time.Duration
	maxConns         int
	internalPrefixes []string
	now              func() time.Time

	acquireWait prometheus.Histogram
	connHold    prometheus.Histogram

	mu sync.Mutex
	// held maps every connection currently acquired through Acquire to when
	// and by whom. Keys are weak so a connection that leaves the pool without
	// Release (Conn.Hijack, the only such path in pgxpool) does not pin its
	// entry or its *pgx.Conn forever; see pruneLocked.
	held map[weak.Pointer[pgx.Conn]]heldConn
}

type heldConn struct {
	acquiredAt time.Time
	callers    []uintptr
}

// acquireKey carries the per-Acquire acquireTrace from TraceAcquireStart to
// TraceAcquireEnd. It is distinct from SlowQueryTracer's contextKey.
type acquireKey struct{}

type acquireTrace struct {
	start   time.Time
	cancel  context.CancelFunc
	callers []uintptr
}

func newSlowQueryTracer(opts poolTracerOptions) *SlowQueryTracer {
	var sqOpts []SlowQueryOption
	if opts.hashSlowQuerySQL {
		sqOpts = append(sqOpts, WithSlowQuerySQLHash())
	}
	return NewSlowQueryTracer(opts.logger, opts.slowQueryThreshold, sqOpts...)
}

func newPoolTracer(opts poolTracerOptions) *poolTracer {
	logger := opts.logger
	if logger == nil {
		logger = slog.Default()
	}
	prefixes := opts.internalPrefixes
	if prefixes == nil {
		prefixes = defaultInternalPrefixes
	}
	return &poolTracer{
		SlowQueryTracer:  newSlowQueryTracer(opts),
		extra:            opts.extra,
		logger:           logger,
		acquireTimeout:   opts.acquireTimeout,
		leakThreshold:    opts.leakThreshold,
		maxConns:         opts.maxConns,
		internalPrefixes: prefixes,
		now:              time.Now,
		acquireWait: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: acquireWaitMetricName, Help: acquireWaitMetricHelp, Buckets: poolHistogramBuckets,
		}),
		connHold: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: connHoldMetricName, Help: connHoldMetricHelp, Buckets: poolHistogramBuckets,
		}),
		held: make(map[weak.Pointer[pgx.Conn]]heldConn),
	}
}

// noAcquireTimeoutKey marks a context whose acquire must not get the
// AcquireTimeout deadline; see withoutAcquireTimeout.
type noAcquireTimeoutKey struct{}

// withoutAcquireTimeout returns ctx marked so TraceAcquireStart adds no
// AcquireTimeout deadline. NewPool uses it for its startup Ping: that acquire
// dials the first connection, which may take up to ConnectTimeout against a
// database that is still waking up, and AcquireTimeout is meant for requests
// queuing on a full pool, not for the boot.
func withoutAcquireTimeout(ctx context.Context) context.Context {
	return context.WithValue(ctx, noAcquireTimeoutKey{}, true)
}

// TraceAcquireStart implements pgxpool.AcquireTracer. pgxpool uses the
// returned context for the rest of Acquire, so a deadline added here bounds
// the wait for a free connection. The cancel func travels in the context and
// TraceAcquireEnd, which pgxpool always calls (deferred), runs it.
func (t *poolTracer) TraceAcquireStart(ctx context.Context, pool *pgxpool.Pool, data pgxpool.TraceAcquireStartData) context.Context {
	trace := &acquireTrace{start: t.now()}
	if t.acquireTimeout > 0 && ctx.Value(noAcquireTimeoutKey{}) == nil {
		ctx, trace.cancel = context.WithTimeout(ctx, t.acquireTimeout)
	}
	if t.leakThreshold > 0 {
		pcs := make([]uintptr, maxCallerFrames)
		// Skip runtime.Callers and this method.
		trace.callers = pcs[:runtime.Callers(2, pcs)]
	}
	return t.extraAcquireStart(context.WithValue(ctx, acquireKey{}, trace), pool, data)
}

// TraceAcquireEnd implements pgxpool.AcquireTracer.
func (t *poolTracer) TraceAcquireEnd(ctx context.Context, pool *pgxpool.Pool, data pgxpool.TraceAcquireEndData) {
	defer t.extraAcquireEnd(ctx, pool, data)
	trace, ok := ctx.Value(acquireKey{}).(*acquireTrace)
	if !ok {
		return
	}
	if trace.cancel != nil {
		trace.cancel()
	}
	end := t.now()
	t.acquireWait.Observe(end.Sub(trace.start).Seconds())

	if data.Err != nil || data.Conn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.held[weak.Make(data.Conn)] = heldConn{acquiredAt: end, callers: trace.callers}
	t.pruneLocked()
}

// pruneLocked drops entries whose connection was garbage collected. pgxpool
// calls TraceRelease on every Release, including one that then destroys the
// connection, and destroys connections it never handed out before
// TraceAcquireEnd, so the only acquired connection that never reaches
// TraceRelease is one taken with Conn.Hijack. Those are the only reason the
// map can outgrow MaxConns, so the scan runs only then. A hijacked connection
// still in use keeps its entry until its owner drops it.
func (t *poolTracer) pruneLocked() {
	if len(t.held) <= t.maxConns {
		return
	}
	for k := range t.held {
		if k.Value() == nil {
			delete(t.held, k)
		}
	}
}

// TraceRelease implements pgxpool.ReleaseTracer. It always removes the
// connection's entry, records how long it was held and, past leakThreshold,
// logs who acquired it. It never logs SQL or arguments.
func (t *poolTracer) TraceRelease(pool *pgxpool.Pool, data pgxpool.TraceReleaseData) {
	defer t.extraRelease(pool, data)
	if data.Conn == nil {
		return
	}
	key := weak.Make(data.Conn)
	t.mu.Lock()
	entry, ok := t.held[key]
	delete(t.held, key)
	t.mu.Unlock()
	if !ok {
		return
	}

	hold := t.now().Sub(entry.acquiredAt)
	t.connHold.Observe(hold.Seconds())
	if t.leakThreshold <= 0 || hold <= t.leakThreshold {
		return
	}

	fn, file, line := firstExternalFrame(entry.callers, t.internalPrefixes)
	t.logger.Warn("database connection held longer than the leak threshold",
		"hold", hold,
		"threshold", t.leakThreshold,
		"caller_func", fn,
		"caller", fmt.Sprintf("%s:%d", file, line),
	)
}

// firstExternalFrame resolves pcs and returns the first frame outside the
// internal prefixes: the code that asked for the connection.
func firstExternalFrame(pcs []uintptr, internalPrefixes []string) (fn, file string, line int) {
	if len(pcs) == 0 {
		return "unknown", "unknown", 0
	}
	frames := runtime.CallersFrames(pcs)
	for {
		frame, more := frames.Next()
		if !isInternalFrame(frame.Function, internalPrefixes) {
			return frame.Function, frame.File, frame.Line
		}
		if !more {
			return "unknown", "unknown", 0
		}
	}
}

func isInternalFrame(function string, internalPrefixes []string) bool {
	for _, p := range internalPrefixes {
		if strings.HasPrefix(function, p) {
			return true
		}
	}
	return false
}
