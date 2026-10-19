package postgres

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type extraKey struct{}

// recordingTracer implements every pgx and pgxpool tracer hook and records
// the order of the calls, and whether each End saw the ctx its Start made.
type recordingTracer struct {
	calls []string
}

func (r *recordingTracer) start(ctx context.Context, name string) context.Context {
	r.calls = append(r.calls, name)
	return context.WithValue(ctx, extraKey{}, name)
}

func (r *recordingTracer) end(ctx context.Context, name string) {
	v, _ := ctx.Value(extraKey{}).(string)
	r.calls = append(r.calls, name+"("+v+")")
}

func (r *recordingTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return r.start(ctx, "query-start")
}
func (r *recordingTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	r.end(ctx, "query-end")
}
func (r *recordingTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return r.start(ctx, "batch-start")
}
func (r *recordingTracer) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchQueryData) {
	r.end(ctx, "batch-query")
}
func (r *recordingTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchEndData) {
	r.end(ctx, "batch-end")
}
func (r *recordingTracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceCopyFromStartData) context.Context {
	return r.start(ctx, "copy-start")
}
func (r *recordingTracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceCopyFromEndData) {
	r.end(ctx, "copy-end")
}
func (r *recordingTracer) TracePrepareStart(ctx context.Context, _ *pgx.Conn, _ pgx.TracePrepareStartData) context.Context {
	return r.start(ctx, "prepare-start")
}
func (r *recordingTracer) TracePrepareEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TracePrepareEndData) {
	r.end(ctx, "prepare-end")
}
func (r *recordingTracer) TraceConnectStart(ctx context.Context, _ pgx.TraceConnectStartData) context.Context {
	return r.start(ctx, "connect-start")
}
func (r *recordingTracer) TraceConnectEnd(ctx context.Context, _ pgx.TraceConnectEndData) {
	r.end(ctx, "connect-end")
}
func (r *recordingTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	return r.start(ctx, "acquire-start")
}
func (r *recordingTracer) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireEndData) {
	r.end(ctx, "acquire-end")
}
func (r *recordingTracer) TraceRelease(_ *pgxpool.Pool, _ pgxpool.TraceReleaseData) {
	r.calls = append(r.calls, "release")
}

// queryOnlyTracer implements only pgx.QueryTracer.
type queryOnlyTracer struct{ starts, ends int }

func (q *queryOnlyTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.starts++
	return ctx
}
func (q *queryOnlyTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) { q.ends++ }

func TestPoolTracer_ImplementsEveryTracerHook(t *testing.T) {
	tr, _ := newTestTracer(t, poolTracerOptions{})
	var _ pgx.BatchTracer = tr
	var _ pgx.CopyFromTracer = tr
	var _ pgx.PrepareTracer = tr
	var _ pgx.ConnectTracer = tr
}

// The extra tracer sees every hook it implements, each End with the ctx its
// own Start returned, while vogel's own behavior (slow queries, acquire
// timeout) keeps working around it.
func TestPoolTracer_ForwardsEveryHookToTheExtraTracer(t *testing.T) {
	extra := &recordingTracer{}
	tr, _ := newTestTracer(t, poolTracerOptions{extra: extra})
	ctx := context.Background()

	qctx := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tr.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{})
	bctx := tr.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{})
	tr.TraceBatchQuery(bctx, nil, pgx.TraceBatchQueryData{})
	tr.TraceBatchEnd(bctx, nil, pgx.TraceBatchEndData{})
	cctx := tr.TraceCopyFromStart(ctx, nil, pgx.TraceCopyFromStartData{})
	tr.TraceCopyFromEnd(cctx, nil, pgx.TraceCopyFromEndData{})
	pctx := tr.TracePrepareStart(ctx, nil, pgx.TracePrepareStartData{})
	tr.TracePrepareEnd(pctx, nil, pgx.TracePrepareEndData{})
	nctx := tr.TraceConnectStart(ctx, pgx.TraceConnectStartData{})
	tr.TraceConnectEnd(nctx, pgx.TraceConnectEndData{})
	actx := tr.TraceAcquireStart(ctx, nil, pgxpool.TraceAcquireStartData{})
	tr.TraceAcquireEnd(actx, nil, pgxpool.TraceAcquireEndData{})
	tr.TraceRelease(nil, pgxpool.TraceReleaseData{})

	assert.Equal(t, []string{
		"query-start", "query-end(query-start)",
		"batch-start", "batch-query(batch-start)", "batch-end(batch-start)",
		"copy-start", "copy-end(copy-start)",
		"prepare-start", "prepare-end(prepare-start)",
		"connect-start", "connect-end(connect-start)",
		"acquire-start", "acquire-end(acquire-start)",
		"release",
	}, extra.calls)
}

// The extra tracer's span is open while vogel's slow-query line is logged, so
// a context-aware handler can attach the span's IDs to it.
func TestPoolTracer_SlowQueryIsLoggedInsideTheExtraSpan(t *testing.T) {
	var seen string
	logger := ctxValueLogger(func(ctx context.Context) { seen, _ = ctx.Value(extraKey{}).(string) })
	tr, _ := newTestTracer(t, poolTracerOptions{extra: &recordingTracer{}, logger: logger})

	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	assert.Equal(t, "query-start", seen)
}

// A tracer implementing only pgx.QueryTracer gets query hooks; the others are
// no-ops that return ctx unchanged.
func TestPoolTracer_ExtraWithQueryHooksOnly(t *testing.T) {
	extra := &queryOnlyTracer{}
	tr, _ := newTestTracer(t, poolTracerOptions{extra: extra})
	ctx := context.Background()

	tr.TraceQueryEnd(tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{}), nil, pgx.TraceQueryEndData{})
	assert.Equal(t, ctx, tr.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{}))
	tr.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{})
	tr.TraceConnectEnd(tr.TraceConnectStart(ctx, pgx.TraceConnectStartData{}), pgx.TraceConnectEndData{})

	assert.Equal(t, 1, extra.starts)
	assert.Equal(t, 1, extra.ends)
}

func TestNewPoolConfig_InstallsTheExtraTracer(t *testing.T) {
	extra := &queryOnlyTracer{}
	poolConfig, err := newPoolConfig(Config{
		URL:    "postgres://u:p@localhost:5432/db?sslmode=disable",
		Tracer: extra,
	}, discardLogger())
	require.NoError(t, err)

	tr, ok := poolConfig.ConnConfig.Tracer.(*poolTracer)
	require.True(t, ok)
	assert.Same(t, extra, tr.extra)
}

// ctxValueLogger returns a logger whose handler calls fn with each record's
// context.
func ctxValueLogger(fn func(context.Context)) *slog.Logger {
	return slog.New(ctxHandler{fn: fn})
}

type ctxHandler struct{ fn func(context.Context) }

func (h ctxHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h ctxHandler) Handle(ctx context.Context, _ slog.Record) error { //nolint:gocritic // slog.Handler signature
	h.fn(ctx)
	return nil
}
func (h ctxHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h ctxHandler) WithGroup(string) slog.Handler      { return h }
