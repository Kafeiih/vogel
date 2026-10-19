package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a manually advanced clock for hold/wait measurements.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time             { return c.t }
func (c *fakeClock) advance(d time.Duration)    { c.t = c.t.Add(d) }
func newFakeClock() *fakeClock                  { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }
func discardLogger() *slog.Logger               { return slog.New(slog.DiscardHandler) }
func jsonLogger(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewJSONHandler(buf, nil)) }

func newTestTracer(t *testing.T, opts poolTracerOptions) (*poolTracer, *fakeClock) {
	t.Helper()
	if opts.logger == nil {
		opts.logger = discardLogger()
	}
	tr := newPoolTracer(opts)
	clock := newFakeClock()
	tr.now = clock.now
	return tr, clock
}

func histogramSnapshot(t *testing.T, h prometheus.Histogram) (count uint64, sum float64) {
	t.Helper()
	var m dto.Metric
	require.NoError(t, h.Write(&m))
	return m.GetHistogram().GetSampleCount(), m.GetHistogram().GetSampleSum()
}

func heldCount(tr *poolTracer) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return len(tr.held)
}

func TestPoolTracer_ImplementsPgxTracerInterfaces(t *testing.T) {
	tr, _ := newTestTracer(t, poolTracerOptions{})
	var _ pgx.QueryTracer = tr
	var _ pgxpool.AcquireTracer = tr
	var _ pgxpool.ReleaseTracer = tr
}

func TestPoolTracer_AcquireTimeout_BoundsContextAndCancelsOnEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  pgxpool.TraceAcquireEndData
	}{
		{"successful acquire", pgxpool.TraceAcquireEndData{Conn: new(pgx.Conn)}},
		{"failed acquire", pgxpool.TraceAcquireEndData{Err: errors.New("boom")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, _ := newTestTracer(t, poolTracerOptions{acquireTimeout: 2 * time.Second})

			before := time.Now()
			ctx := tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})

			deadline, ok := ctx.Deadline()
			require.True(t, ok, "acquire context must carry the AcquireTimeout deadline")
			assert.WithinDuration(t, before.Add(2*time.Second), deadline, 500*time.Millisecond)
			require.NoError(t, ctx.Err(), "deadline must not fire before the acquire ends")

			tr.TraceAcquireEnd(ctx, nil, tc.end)

			assert.ErrorIs(t, ctx.Err(), context.Canceled,
				"TraceAcquireEnd must cancel the derived context so its timer is released")
		})
	}
}

func TestPoolTracer_AcquireTimeout_KeepsEarlierCallerDeadline(t *testing.T) {
	tr, _ := newTestTracer(t, poolTracerOptions{acquireTimeout: time.Hour})
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	parentDeadline, _ := parent.Deadline()

	ctx := tr.TraceAcquireStart(parent, nil, pgxpool.TraceAcquireStartData{})
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.Equal(t, parentDeadline, deadline, "a shorter caller deadline must win")
	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Conn: new(pgx.Conn)})
}

func TestPoolTracer_ZeroAcquireTimeout_LeavesContextUnbounded(t *testing.T) {
	tr, _ := newTestTracer(t, poolTracerOptions{})

	ctx := tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
	_, ok := ctx.Deadline()
	assert.False(t, ok, "zero AcquireTimeout must not add a deadline")

	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Conn: new(pgx.Conn)})
	assert.NoError(t, ctx.Err(), "nothing to cancel when no timeout was added")
}

func TestPoolTracer_ObservesAcquireWait(t *testing.T) {
	tr, clock := newTestTracer(t, poolTracerOptions{})

	ctx := tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
	clock.advance(250 * time.Millisecond)
	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Conn: new(pgx.Conn)})

	ctx = tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
	clock.advance(time.Second)
	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Err: context.DeadlineExceeded})

	count, sum := histogramSnapshot(t, tr.acquireWait)
	assert.Equal(t, uint64(2), count, "failed acquires are observed too, so timeouts show in the wait histogram")
	assert.InDelta(t, 1.25, sum, 1e-9)
}

func TestPoolTracer_ObservesHoldAndRemovesEntryOnRelease(t *testing.T) {
	tr, clock := newTestTracer(t, poolTracerOptions{})
	conn := new(pgx.Conn)

	ctx := tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Conn: conn})
	require.Equal(t, 1, heldCount(tr))

	clock.advance(1500 * time.Millisecond)
	tr.TraceRelease(nil, pgxpool.TraceReleaseData{Conn: conn})

	assert.Equal(t, 0, heldCount(tr), "release must always remove the hold entry")
	count, sum := histogramSnapshot(t, tr.connHold)
	assert.Equal(t, uint64(1), count)
	assert.InDelta(t, 1.5, sum, 1e-9)
}

func TestPoolTracer_FailedAcquire_TracksNothing(t *testing.T) {
	tr, _ := newTestTracer(t, poolTracerOptions{leakThreshold: time.Second})

	ctx := tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Err: errors.New("boom")})

	assert.Equal(t, 0, heldCount(tr))
}

func TestPoolTracer_ReleaseOfUntrackedConn_IsIgnored(t *testing.T) {
	tr, _ := newTestTracer(t, poolTracerOptions{leakThreshold: time.Second})

	assert.NotPanics(t, func() {
		tr.TraceRelease(nil, pgxpool.TraceReleaseData{Conn: new(pgx.Conn)})
	})
	count, _ := histogramSnapshot(t, tr.connHold)
	assert.Equal(t, uint64(0), count)
}

// A connection taken out of the pool with Conn.Hijack never reaches
// TraceRelease. Its entry must not stay in the map forever: once the
// hijacked *pgx.Conn is garbage collected, the next acquire prunes it.
func TestPoolTracer_HijackedConnEntry_IsPrunedAfterCollection(t *testing.T) {
	tr, _ := newTestTracer(t, poolTracerOptions{maxConns: 1})

	func() {
		hijacked := new(pgx.Conn)
		ctx := tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
		tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Conn: hijacked})
	}()
	require.Equal(t, 1, heldCount(tr))

	runtime.GC()
	runtime.GC()

	live := new(pgx.Conn)
	ctx := tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Conn: live})

	assert.Equal(t, 1, heldCount(tr), "the collected hijacked entry must be pruned, the live one kept")
	tr.TraceRelease(nil, pgxpool.TraceReleaseData{Conn: live})
	assert.Equal(t, 0, heldCount(tr))
	runtime.KeepAlive(live)
}

type leakLog struct {
	Level      string `json:"level"`
	Msg        string `json:"msg"`
	Hold       int64  `json:"hold"`
	Threshold  int64  `json:"threshold"`
	CallerFunc string `json:"caller_func"`
	Caller     string `json:"caller"`
}

func decodeLogs(t *testing.T, buf *bytes.Buffer) []leakLog {
	t.Helper()
	var logs []leakLog
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var l leakLog
		require.NoError(t, json.Unmarshal([]byte(line), &l))
		logs = append(logs, l)
	}
	return logs
}

// acquireFromHere stands in for application code acquiring a connection, so
// the test can assert the WARN names this exact frame.
func acquireFromHere(tr *poolTracer, conn *pgx.Conn) {
	ctx := tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Conn: conn})
}

func TestPoolTracer_LeakThreshold_WarnsWithAcquiringCaller(t *testing.T) {
	var buf bytes.Buffer
	tr, clock := newTestTracer(t, poolTracerOptions{
		logger:        jsonLogger(&buf),
		leakThreshold: 10 * time.Second,
		// Tests live in package postgres, which the default list skips;
		// keep only pgx so this file's frames count as the caller.
		internalPrefixes: []string{"github.com/jackc/pgx/"},
	})
	conn := new(pgx.Conn)

	acquireFromHere(tr, conn)
	clock.advance(12 * time.Second)
	tr.TraceRelease(nil, pgxpool.TraceReleaseData{Conn: conn})

	logs := decodeLogs(t, &buf)
	require.Len(t, logs, 1)
	l := logs[0]
	assert.Equal(t, "WARN", l.Level)
	assert.Equal(t, "database connection held longer than the leak threshold", l.Msg)
	assert.Equal(t, int64(12*time.Second), l.Hold, "slog JSON encodes durations in nanoseconds")
	assert.Equal(t, int64(10*time.Second), l.Threshold)
	assert.True(t, strings.HasSuffix(l.CallerFunc, "postgres.acquireFromHere"), "caller_func = %q", l.CallerFunc)
	assert.Regexp(t, `pool_tracer_test\.go:\d+$`, l.Caller)
	assert.NotContains(t, buf.String(), "sql", "the leak WARN must never carry SQL")
}

func TestPoolTracer_LeakThreshold_NoWarnAtOrBelowThreshold(t *testing.T) {
	var buf bytes.Buffer
	tr, clock := newTestTracer(t, poolTracerOptions{logger: jsonLogger(&buf), leakThreshold: 10 * time.Second})
	conn := new(pgx.Conn)

	acquireFromHere(tr, conn)
	clock.advance(10 * time.Second)
	tr.TraceRelease(nil, pgxpool.TraceReleaseData{Conn: conn})

	assert.Empty(t, buf.String())
}

func TestPoolTracer_ZeroLeakThreshold_NeitherWarnsNorCapturesCaller(t *testing.T) {
	var buf bytes.Buffer
	tr, clock := newTestTracer(t, poolTracerOptions{logger: jsonLogger(&buf)})
	conn := new(pgx.Conn)

	ctx := tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
	trace, ok := ctx.Value(acquireKey{}).(*acquireTrace)
	require.True(t, ok)
	assert.Nil(t, trace.callers, "the stack must only be captured when LeakThreshold > 0")
	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Conn: conn})

	clock.advance(time.Hour)
	tr.TraceRelease(nil, pgxpool.TraceReleaseData{Conn: conn})

	assert.Empty(t, buf.String())
	count, _ := histogramSnapshot(t, tr.connHold)
	assert.Equal(t, uint64(1), count, "hold is still measured without a leak threshold")
}

func TestIsInternalFrame_DefaultPrefixes(t *testing.T) {
	for _, tc := range []struct {
		fn       string
		internal bool
	}{
		{"github.com/jackc/pgx/v5/pgxpool.(*Pool).Acquire", true},
		{"github.com/jackc/pgx/v5.(*Conn).Query", true},
		{"github.com/jackc/puddle/v2.(*Pool[...]).Acquire", true},
		{"github.com/kafeiih/vogel/postgres.(*poolTracer).TraceAcquireStart", true},
		{"github.com/kafeiih/vogel/pgxtx.WithTx.func1", true},
		{"github.com/kafeiih/vogel/postgres_test.TestLeak", false},
		{"github.com/kafeiih/vogel/pgxtxfoo.Run", false},
		{"example.com/app/internal/audit.(*Repo).Insert", false},
	} {
		t.Run(tc.fn, func(t *testing.T) {
			assert.Equal(t, tc.internal, isInternalFrame(tc.fn, defaultInternalPrefixes))
		})
	}
}

// The slow-query behavior the pool tracer composes must stay exactly that of
// SlowQueryTracer.
func TestPoolTracer_DelegatesSlowQueryTracing(t *testing.T) {
	var buf bytes.Buffer
	tr := newPoolTracer(poolTracerOptions{
		logger:             jsonLogger(&buf),
		slowQueryThreshold: 0,
	})

	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1", Args: []any{1, 2}})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	out := buf.String()
	assert.Contains(t, out, `"msg":"slow query detected"`)
	assert.Contains(t, out, `"sql":"SELECT 1"`)
	assert.Contains(t, out, `"args_count":2`)
}

func TestNewPoolConfig_InstallsPoolTracer(t *testing.T) {
	poolConfig, err := newPoolConfig(Config{
		URL:            "postgres://u:p@localhost:5432/db?sslmode=disable",
		MaxConns:       3,
		AcquireTimeout: 3 * time.Second,
		LeakThreshold:  10 * time.Second,
	}, discardLogger())
	require.NoError(t, err)

	tr, ok := poolConfig.ConnConfig.Tracer.(*poolTracer)
	require.True(t, ok, "Tracer = %T, want *poolTracer", poolConfig.ConnConfig.Tracer)
	assert.Equal(t, 3*time.Second, tr.acquireTimeout)
	assert.Equal(t, 10*time.Second, tr.leakThreshold)
	assert.Equal(t, slowQueryThreshold, tr.threshold, "slow-query threshold unchanged")
	assert.Equal(t, 3, tr.maxConns)
}

func TestNewPool_NegativeAcquireTimeoutOrLeakThreshold_FailsBeforeConnecting(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"acquire timeout", Config{AcquireTimeout: -time.Second}, "AcquireTimeout"},
		{"leak threshold", Config{LeakThreshold: -time.Second}, "LeakThreshold"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.URL = "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1"
			pool, err := NewPool(t.Context(), tc.cfg, discardLogger())
			require.Error(t, err)
			assert.Nil(t, pool)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "must not be negative")
		})
	}
}

// NewPool's startup Ping may have to dial the first connection, which can
// take up to ConnectTimeout against a database waking up. It is marked so
// AcquireTimeout, meant for requests queuing on a full pool, does not cut it.
func TestPoolTracer_StartupAcquire_IgnoresAcquireTimeout(t *testing.T) {
	tr, _ := newTestTracer(t, poolTracerOptions{acquireTimeout: 2 * time.Second})

	ctx := tr.TraceAcquireStart(withoutAcquireTimeout(context.Background()), nil, pgxpool.TraceAcquireStartData{})
	_, ok := ctx.Deadline()
	assert.False(t, ok, "the startup acquire must not get the AcquireTimeout deadline")

	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Conn: new(pgx.Conn)})
}

func histogramExemplar(t *testing.T, h prometheus.Histogram) map[string]string {
	t.Helper()
	var m dto.Metric
	require.NoError(t, h.Write(&m))
	for _, b := range m.GetHistogram().GetBucket() {
		if e := b.GetExemplar(); e != nil {
			labels := map[string]string{}
			for _, l := range e.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			return labels
		}
	}
	return nil
}

type traceKey struct{}

// fakeTraceIDs reads the IDs a test put in the context with traceKey.
func fakeTraceIDs(ctx context.Context) (string, string) {
	ids, _ := ctx.Value(traceKey{}).([2]string)
	return ids[0], ids[1]
}

// With traceIDs, both pool histograms carry the acquiring request's trace as
// an exemplar, and so does the leak warning, although the release that
// emits it has no context.
func TestPoolTracer_TraceIDs_ExemplarsAndLeakWarning(t *testing.T) {
	var buf bytes.Buffer
	tr, clock := newTestTracer(t, poolTracerOptions{
		logger:           jsonLogger(&buf),
		leakThreshold:    time.Second,
		traceIDs:         fakeTraceIDs,
		internalPrefixes: []string{"github.com/jackc/pgx/"},
	})
	conn := new(pgx.Conn)
	ctx := context.WithValue(context.Background(), traceKey{}, [2]string{"0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331"})

	ctx = tr.TraceAcquireStart(ctx, nil, pgxpool.TraceAcquireStartData{})
	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Conn: conn})
	clock.advance(2 * time.Second)
	tr.TraceRelease(nil, pgxpool.TraceReleaseData{Conn: conn})

	want := map[string]string{"trace_id": "0af7651916cd43dd8448eb211c80319c", "span_id": "b7ad6b7169203331"}
	assert.Equal(t, want, histogramExemplar(t, tr.acquireWait))
	assert.Equal(t, want, histogramExemplar(t, tr.connHold))
	var line map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line))
	assert.Equal(t, "0af7651916cd43dd8448eb211c80319c", line["trace_id"])
	assert.Equal(t, "b7ad6b7169203331", line["span_id"])
}

// Without traceIDs, or outside a trace, nothing changes: no exemplar and no
// trace keys on the leak warning.
func TestPoolTracer_NoTrace_NoExemplarNorTraceKeys(t *testing.T) {
	for name, fn := range map[string]func(context.Context) (string, string){"no option": nil, "no trace": fakeTraceIDs} {
		var buf bytes.Buffer
		tr, clock := newTestTracer(t, poolTracerOptions{logger: jsonLogger(&buf), leakThreshold: time.Second, traceIDs: fn})
		conn := new(pgx.Conn)
		ctx := tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
		tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Conn: conn})
		clock.advance(2 * time.Second)
		tr.TraceRelease(nil, pgxpool.TraceReleaseData{Conn: conn})

		assert.Nil(t, histogramExemplar(t, tr.acquireWait), name)
		assert.Nil(t, histogramExemplar(t, tr.connHold), name)
		assert.NotContains(t, buf.String(), "trace_id", name)
	}
}
