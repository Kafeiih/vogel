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
)

// contextHandler wraps a slog.Handler and adds request_id and user_id from
// the record's context, read from reqctx, to every record. It makes the
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
// from here.
type contextHandler struct {
	inner slog.Handler

	// hasRequestID and hasUserID record whether the key is already bound at
	// the top level through WithAttrs.
	hasRequestID bool
	hasUserID    bool
	// grouped is set once WithGroup opened a group: attributes bound after
	// it are qualified by the group and no longer collide with the
	// top-level keys.
	grouped bool
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx == nil || (h.hasRequestID && h.hasUserID) {
		return h.inner.Handle(ctx, r)
	}

	hasRequestID, hasUserID := h.hasRequestID, h.hasUserID
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case requestIDKey:
			hasRequestID = true
		case userIDKey:
			hasUserID = true
		}
		return !hasRequestID || !hasUserID
	})

	if !hasRequestID {
		if id := reqctx.RequestIDFromContext(ctx); id != "" {
			r.AddAttrs(slog.String(requestIDKey, id))
		}
	}
	if !hasUserID {
		if id := reqctx.UserIDFromContext(ctx); id != "" {
			r.AddAttrs(slog.String(userIDKey, id))
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.inner = h.inner.WithAttrs(attrs)
	if !h.grouped {
		for _, a := range attrs {
			switch a.Key {
			case requestIDKey:
				next.hasRequestID = true
			case userIDKey:
				next.hasUserID = true
			}
		}
	}
	return &next
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	next := *h
	next.inner = h.inner.WithGroup(name)
	if name != "" {
		next.grouped = true
	}
	return &next
}
