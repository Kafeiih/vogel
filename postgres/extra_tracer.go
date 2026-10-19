package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This file forwards every pgx and pgxpool tracer hook to Config.Tracer, the
// optional extra tracer (an OpenTelemetry pgx tracer, for instance),
// around poolTracer's own behavior. Start hooks run the extra tracer first and
// End hooks last, so its span encloses vogel's work on the same hook: the
// slow-query WARN is logged while the query span is still open. Each End gets
// the ctx its Start returned, so the two tracers' context values never mix.

func (t *poolTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if t.extra != nil {
		ctx = t.extra.TraceQueryStart(ctx, conn, data)
	}
	return t.SlowQueryTracer.TraceQueryStart(ctx, conn, data)
}

func (t *poolTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	t.SlowQueryTracer.TraceQueryEnd(ctx, conn, data)
	if t.extra != nil {
		t.extra.TraceQueryEnd(ctx, conn, data)
	}
}

func (t *poolTracer) TraceBatchStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	if bt, ok := t.extra.(pgx.BatchTracer); ok {
		return bt.TraceBatchStart(ctx, conn, data)
	}
	return ctx
}

func (t *poolTracer) TraceBatchQuery(ctx context.Context, conn *pgx.Conn, data pgx.TraceBatchQueryData) {
	if bt, ok := t.extra.(pgx.BatchTracer); ok {
		bt.TraceBatchQuery(ctx, conn, data)
	}
}

func (t *poolTracer) TraceBatchEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceBatchEndData) {
	if bt, ok := t.extra.(pgx.BatchTracer); ok {
		bt.TraceBatchEnd(ctx, conn, data)
	}
}

func (t *poolTracer) TraceCopyFromStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	if ct, ok := t.extra.(pgx.CopyFromTracer); ok {
		return ct.TraceCopyFromStart(ctx, conn, data)
	}
	return ctx
}

func (t *poolTracer) TraceCopyFromEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceCopyFromEndData) {
	if ct, ok := t.extra.(pgx.CopyFromTracer); ok {
		ct.TraceCopyFromEnd(ctx, conn, data)
	}
}

func (t *poolTracer) TracePrepareStart(ctx context.Context, conn *pgx.Conn, data pgx.TracePrepareStartData) context.Context {
	if pt, ok := t.extra.(pgx.PrepareTracer); ok {
		return pt.TracePrepareStart(ctx, conn, data)
	}
	return ctx
}

func (t *poolTracer) TracePrepareEnd(ctx context.Context, conn *pgx.Conn, data pgx.TracePrepareEndData) {
	if pt, ok := t.extra.(pgx.PrepareTracer); ok {
		pt.TracePrepareEnd(ctx, conn, data)
	}
}

func (t *poolTracer) TraceConnectStart(ctx context.Context, data pgx.TraceConnectStartData) context.Context {
	if ct, ok := t.extra.(pgx.ConnectTracer); ok {
		return ct.TraceConnectStart(ctx, data)
	}
	return ctx
}

func (t *poolTracer) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	if ct, ok := t.extra.(pgx.ConnectTracer); ok {
		ct.TraceConnectEnd(ctx, data)
	}
}

// extraAcquireStart and the two helpers below are called by poolTracer's own
// Acquire and Release hooks.
func (t *poolTracer) extraAcquireStart(ctx context.Context, pool *pgxpool.Pool, data pgxpool.TraceAcquireStartData) context.Context {
	if at, ok := t.extra.(pgxpool.AcquireTracer); ok {
		return at.TraceAcquireStart(ctx, pool, data)
	}
	return ctx
}

func (t *poolTracer) extraAcquireEnd(ctx context.Context, pool *pgxpool.Pool, data pgxpool.TraceAcquireEndData) {
	if at, ok := t.extra.(pgxpool.AcquireTracer); ok {
		at.TraceAcquireEnd(ctx, pool, data)
	}
}

func (t *poolTracer) extraRelease(pool *pgxpool.Pool, data pgxpool.TraceReleaseData) {
	if rt, ok := t.extra.(pgxpool.ReleaseTracer); ok {
		rt.TraceRelease(pool, data)
	}
}
