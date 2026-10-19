package logger

import (
	"context"
	"log/slog"

	"github.com/kafeiih/vogel/reqctx"
)

// Keys contextHandler fills from reqctx. Logger.WithContext binds the same
// keys, which is what lets the two paths dedupe against each other.
const (
	requestIDKey = "request_id"
	userIDKey    = "user_id"
	traceIDKey   = "trace_id"
	spanIDKey    = "span_id"
)

// contextHandler wraps a slog.Handler and adds request_id and user_id from
// the record's context, read from reqctx, to every record, plus trace_id and
// span_id when WithTraceIDs installed a reader for them. It makes the
// *Context methods (InfoContext, ErrorContext, and package-level slog ones
// once the Logger is installed with slog.SetDefault) correlate a line with
// its request without the caller remembering Logger.WithContext.
//
// A key is added only when ctx carries a non-empty value, so a line logged
// outside a request keeps its shape, and only when neither the record nor
// the logger (at the top level, by WithContext or a plain With) already
// carries that key, so a line never has it twice and an explicit value wins.
//
// Inside a group (WithGroup), keys are added within the group, as every
// record attribute is; slog offers no way to place them at the top level
// from here. The duplicate check follows them there: only keys bound inside
// the innermost open group collide with them.
type contextHandler struct {
	inner    slog.Handler
	traceIDs TraceIDsFunc // nil without WithTraceIDs

	// hasRequestID, hasUserID, hasTraceID and hasSpanID record whether the
	// key is already bound, through WithAttrs, at the level where Handle adds
	// attributes: the top level, or the innermost group once WithGroup
	// opened one.
	hasRequestID bool
	hasUserID    bool
	hasTraceID   bool
	hasSpanID    bool
}

// complete reports whether every key Handle could add is already bound.
func (h *contextHandler) complete() bool {
	return h.hasRequestID && h.hasUserID && (h.traceIDs == nil || (h.hasTraceID && h.hasSpanID))
}

// bind records that key is bound at the current level.
func (h *contextHandler) bind(key string) {
	switch key {
	case requestIDKey:
		h.hasRequestID = true
	case userIDKey:
		h.hasUserID = true
	case traceIDKey:
		h.hasTraceID = true
	case spanIDKey:
		h.hasSpanID = true
	}
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error { //nolint:gocritic // hugeParam: slog.Handler fixes the by-value Record signature
	if ctx == nil || h.complete() {
		return h.inner.Handle(ctx, r)
	}

	seen := *h
	r.Attrs(func(a slog.Attr) bool {
		seen.bind(a.Key)
		return !seen.complete()
	})

	if !seen.hasRequestID {
		if id := reqctx.RequestIDFromContext(ctx); id != "" {
			r.AddAttrs(slog.String(requestIDKey, id))
		}
	}
	if !seen.hasUserID {
		if id := reqctx.UserIDFromContext(ctx); id != "" {
			r.AddAttrs(slog.String(userIDKey, id))
		}
	}
	if h.traceIDs != nil && (!seen.hasTraceID || !seen.hasSpanID) {
		traceID, spanID := h.traceIDs(ctx)
		if !seen.hasTraceID && traceID != "" {
			r.AddAttrs(slog.String(traceIDKey, traceID))
		}
		if !seen.hasSpanID && spanID != "" {
			r.AddAttrs(slog.String(spanIDKey, spanID))
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.inner = h.inner.WithAttrs(attrs)
	for _, a := range attrs {
		next.bind(a.Key)
	}
	return &next
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	next := *h
	next.inner = h.inner.WithGroup(name)
	if name != "" {
		// Attributes from here on are qualified by the new group, so keys
		// bound outside it no longer collide with the ones Handle adds.
		next.hasRequestID = false
		next.hasUserID = false
		next.hasTraceID = false
		next.hasSpanID = false
	}
	return &next
}
